// Command backfill_minutes reconstructs specific missing intraday minutes for
// the industry/concept work table from the upstream darktradetick curves.
//
// Scope: money columns only. The quote columns of inserted rows stay at their
// zero defaults with quote_available=0, which downstream consumers filter on;
// the tick endpoint does not carry quotes for arbitrary minutes.
//
// Full-day repairs should NOT use this tool: `collect -task end-of-day -date X`
// re-collects a complete day through the production code path instead.
//
// Usage:
//
//	backfill_minutes --db /data/shadowflow.db --trade-date 2026-08-25 --clocks 11:04,11:05,11:06 collect
//	backfill_minutes --db ... --trade-date ... --clocks ... insert
//	backfill_minutes --db ... --trade-date ... --clocks ... verify
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/roiding/shadowflow/internal/datasource/upstream"
)

const tickBaseURL = "https://quotederivates.eastmoney.com/datacenter/darktradetick"

var shanghai = time.FixedZone("Asia/Shanghai", 8*3600)

type board struct {
	RankType string
	Market   int64
	Code     string
	Name     string
}

type point struct {
	RankType        string `json:"rank_type"`
	Market          int64  `json:"market"`
	Code            string `json:"code"`
	Name            string `json:"name"`
	Clock           string `json:"clock"`
	Rank            int64  `json:"rank,omitempty"`
	DarkMoney       int64  `json:"dark_money"`
	RegularMoney    int64  `json:"regular_money"`
	MainMoneyInflow int64  `json:"main_money_inflow"`
}

type payload struct {
	TradeDate string   `json:"trade_date"`
	Clocks    []string `json:"clocks"`
	Points    []point  `json:"points"`
	Errors    []string `json:"errors,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	dbPath := flag.String("db", "", "path to shadowflow.db")
	outDir := flag.String("out", "", "payload directory (default: alongside the database)")
	tradeDate := flag.String("trade-date", "", "trade date in YYYY-MM-DD")
	clocksRaw := flag.String("clocks", "", "comma-separated HH:MM minutes to backfill (Beijing time)")
	flag.Parse()
	if *dbPath == "" || *tradeDate == "" || *clocksRaw == "" {
		flag.Usage()
		return fmt.Errorf("--db, --trade-date and --clocks are required")
	}
	if _, err := time.Parse("2006-01-02", *tradeDate); err != nil {
		return fmt.Errorf("trade date must use YYYY-MM-DD: %w", err)
	}
	clocks := strings.Split(*clocksRaw, ",")
	for index, clock := range clocks {
		clocks[index] = strings.TrimSpace(clock)
		if _, err := time.Parse("15:04", clocks[index]); err != nil {
			return fmt.Errorf("clock %q must use HH:MM: %w", clocks[index], err)
		}
	}
	if *outDir == "" {
		*outDir = filepath.Join(filepath.Dir(*dbPath), "minutes_backfill")
	}
	switch flag.Arg(0) {
	case "collect":
		return collect(*dbPath, *outDir, *tradeDate, clocks)
	case "insert":
		return insert(*dbPath, *outDir, *tradeDate, clocks)
	case "verify":
		return verify(*dbPath, *tradeDate, clocks)
	default:
		return fmt.Errorf("unknown command %q (want collect|insert|verify)", flag.Arg(0))
	}
}

// openDB matches the production writer settings: without a busy timeout every
// lock collision with the live collector was an instant fatal error, and a
// deferred transaction upgrade could fail halfway through the write.
func openDB(path string, readOnly bool) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("database does not exist (the driver would silently create one): %w", err)
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(60000)&_txlock=immediate"
	if readOnly {
		dsn = "file:" + path + "?mode=ro&_pragma=busy_timeout(10000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func collect(dbPath, outDir, tradeDate string, clocks []string) error {
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	boards, err := loadBoards(dbPath, tradeDate)
	if err != nil {
		return err
	}
	log.Printf("loaded %d boards (industry+concept)", len(boards))

	// The shared upstream guard applies the same concurrency cap, rate limit
	// and circuit breaker as production collection; the previous ungoverned
	// 8-way fan-out could trip the breaker for a live collector on the same
	// egress IP.
	guard := upstream.New(&http.Client{Timeout: 20 * time.Second}, upstream.Options{
		MaxConcurrency: 4, RatePerSecond: 8,
	})
	result := payload{TradeDate: tradeDate, Clocks: clocks}
	dateToken := strings.ReplaceAll(tradeDate, "-", "")[2:]
	for _, item := range boards {
		ticks, err := fetchTicks(guard, item, dateToken, clocks)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s %s: %v", item.RankType, item.Code, err))
			continue
		}
		for _, clock := range clocks {
			tick, ok := ticks[clock]
			if !ok {
				result.Errors = append(result.Errors, fmt.Sprintf("%s %s: missing tick %s", item.RankType, item.Code, clock))
				continue
			}
			result.Points = append(result.Points, point{
				RankType: item.RankType, Market: item.Market, Code: item.Code, Name: item.Name,
				Clock: clock, DarkMoney: tick.dark, RegularMoney: tick.regular, MainMoneyInflow: tick.main,
			})
		}
	}
	log.Printf("collected %d points, %d errors", len(result.Points), len(result.Errors))
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	target := filepath.Join(outDir, "payload.json")
	temporary := target + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	log.Printf("payload written to %s", target)
	return nil
}

type tick struct {
	dark    int64
	regular int64
	main    int64
}

type tickResponse struct {
	ErrorID int64 `json:"errid"`
	Data    []struct {
		Time    int64 `json:"time"`
		Dark    int64 `json:"1"`
		Regular int64 `json:"2"`
		Main    int64 `json:"3"`
	} `json:"data"`
}

func fetchTicks(guard *upstream.Guard, item board, dateToken string, clocks []string) (map[string]tick, error) {
	params := url.Values{
		"code": {item.Code}, "market": {strconv.FormatInt(item.Market, 10)},
		"time": {"0"}, "version": {"100"}, "cver": {"11.2.6"},
	}
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
		body, err := guardedGet(guard, tickBaseURL+"?"+params.Encode())
		if err != nil {
			lastErr = err
			continue
		}
		var response tickResponse
		if err := json.NewDecoder(bytes.NewReader(body)).Decode(&response); err != nil {
			lastErr = fmt.Errorf("decode: %w", err)
			continue
		}
		if response.ErrorID != 0 {
			lastErr = fmt.Errorf("upstream error %d", response.ErrorID)
			continue
		}
		result := make(map[string]tick, len(clocks))
		for _, row := range response.Data {
			raw := fmt.Sprintf("%010d", row.Time)
			if raw[:6] != dateToken {
				continue
			}
			clock := raw[6:8] + ":" + raw[8:10]
			found := false
			for _, wanted := range clocks {
				if wanted == clock {
					found = true
					break
				}
			}
			if !found {
				continue
			}
			// The same consistency invariant production enforces: silently
			// inconsistent money fields indicate a schema drift upstream.
			if row.Main != row.Dark+row.Regular {
				return nil, fmt.Errorf("inconsistent money fields at %s", clock)
			}
			if _, duplicate := result[clock]; duplicate {
				return nil, fmt.Errorf("duplicate tick at %s", clock)
			}
			result[clock] = tick{dark: row.Dark, regular: row.Regular, main: row.Main}
		}
		return result, nil
	}
	return nil, lastErr
}

func guardedGet(guard *upstream.Guard, rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "shadowflow/0.1")
	response, err := guard.Do(ctx, request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, 2<<20))
}

func loadBoards(dbPath, tradeDate string) ([]board, error) {
	db, err := openDB(dbPath, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT rank_type,market,code,name FROM rank_snapshot
WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type IN ('industry','concept')
ORDER BY rank_type,rank`, tradeDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var boards []board
	for rows.Next() {
		var item board
		if err := rows.Scan(&item.RankType, &item.Market, &item.Code, &item.Name); err != nil {
			return nil, err
		}
		boards = append(boards, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(boards) == 0 {
		return nil, fmt.Errorf("no daily-close boards for %s; run the day's archive first", tradeDate)
	}
	return boards, nil
}

func insert(dbPath, outDir, tradeDate string, clocks []string) error {
	raw, err := os.ReadFile(filepath.Join(outDir, "payload.json"))
	if err != nil {
		return err
	}
	var data payload
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	// Refuse mismatched payloads and partial collections: inserting a payload
	// collected for another date, or one with fetch errors, would silently
	// write wrong or incomplete minutes.
	if data.TradeDate != tradeDate {
		return fmt.Errorf("payload is for %s, not %s", data.TradeDate, tradeDate)
	}
	if len(data.Errors) > 0 {
		return fmt.Errorf("payload has %d collection errors; re-run collect until clean: %s", len(data.Errors), strings.Join(data.Errors[:min(3, len(data.Errors))], "; "))
	}
	if len(data.Points) == 0 {
		return fmt.Errorf("payload empty (run collect first)")
	}

	byMinute := make(map[string][]*point)
	for index := range data.Points {
		item := &data.Points[index]
		byMinute[item.RankType+"@"+item.Clock] = append(byMinute[item.RankType+"@"+item.Clock], item)
	}
	for key, group := range byMinute {
		sort.Slice(group, func(i, j int) bool {
			if group[i].DarkMoney != group[j].DarkMoney {
				return group[i].DarkMoney > group[j].DarkMoney
			}
			if group[i].Code != group[j].Code {
				return group[i].Code < group[j].Code
			}
			return group[i].Market < group[j].Market
		})
		for rank := range group {
			group[rank].Rank = int64(rank + 1)
		}
		log.Printf("%s: %d rows", key, len(group))
	}

	db, err := openDB(dbPath, false)
	if err != nil {
		return err
	}
	defer db.Close()
	runID := "backfill-minutes-" + newRunSuffix()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO rank_intraday_work
(run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,quote_time,
latest_price_raw,open_price,high_price,low_price,close_price,previous_close,change_value,change_pct,
volume,turnover,turnover_rate,amplitude,quote_available,money_available,dark_money,regular_money,main_money_inflow,
dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,
source_version,source_sort_flag,source_descending,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(trade_date,snapshot_at,rank_type,code) DO UPDATE SET
rank=excluded.rank,dark_money=excluded.dark_money,regular_money=excluded.regular_money,
main_money_inflow=excluded.main_money_inflow,money_available=1,fetched_at=excluded.fetched_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for index := range data.Points {
		item := &data.Points[index]
		at, err := time.ParseInLocation("2006-01-02 15:04", tradeDate+" "+item.Clock, shanghai)
		if err != nil {
			return err
		}
		// Stored timestamps are normalized to UTC 'Z' (see formatTimestamp in
		// repository/sqlite); quote columns stay zero with quote_available=0
		// as the explicit "no quote data" sentinel.
		if _, err := stmt.Exec(runID, at.UTC().Format(time.RFC3339Nano), tradeDate, item.RankType,
			item.Rank, item.Market, item.Code, item.Name, "",
			0, 0, 0, 0, 0, 0, 0, 0,
			0, 0, 0, 0, 0, 1, item.DarkMoney, item.RegularMoney, item.MainMoneyInflow,
			0, 0, 0, 0, 0, "", "",
			100, 6, 1, now); err != nil {
			return fmt.Errorf("insert %s %s %s: %w", item.RankType, item.Code, item.Clock, err)
		}
	}
	// Recompute quality from what is actually stored instead of declaring the
	// day perfect: a partially successful backfill must stay visible in the
	// quality dashboard.
	for _, rankType := range []string{"industry", "concept"} {
		if err := recomputeMinuteQuality(tx, tradeDate, rankType, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("COMMIT ok: %d rows (run_id=%s)", len(data.Points), runID)
	return nil
}

// recomputeMinuteQuality measures collected/missing intraday minutes from the
// work table and updates only the minute-related quality columns.
func recomputeMinuteQuality(tx *sql.Tx, tradeDate, rankType, now string) error {
	rows, err := tx.Query(`SELECT DISTINCT strftime('%H:%M', snapshot_at, '+8 hours')
FROM rank_intraday_work WHERE trade_date=? AND rank_type=? ORDER BY 1`, tradeDate, rankType)
	if err != nil {
		return err
	}
	defer rows.Close()
	collected := make(map[string]struct{}, 240)
	for rows.Next() {
		var minute string
		if err := rows.Scan(&minute); err != nil {
			return err
		}
		collected[minute] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var missing []string
	for _, minute := range expectedMinutes() {
		if _, ok := collected[minute]; !ok {
			missing = append(missing, minute)
		}
	}
	missingJSON, err := json.Marshal(missing)
	if err != nil {
		return err
	}
	if missing == nil {
		missingJSON = []byte("[]")
	}
	result, err := tx.Exec(`UPDATE research_quality SET collected_minutes=?,missing_minutes_json=?,compacted_at=?
WHERE trade_date=? AND rank_type=?`, len(collected), string(missingJSON), now, tradeDate, rankType)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		log.Printf("note: no research_quality row for %s %s yet (compaction has not run); minute counts not recorded", tradeDate, rankType)
	}
	log.Printf("%s quality: collected_minutes=%d missing=%d", rankType, len(collected), len(missing))
	return nil
}

func expectedMinutes() []string {
	var result []string
	appendRange := func(startHour, startMinute, endHour, endMinute int) {
		current := time.Date(2000, 1, 1, startHour, startMinute, 0, 0, time.UTC)
		end := time.Date(2000, 1, 1, endHour, endMinute, 0, 0, time.UTC)
		for !current.After(end) {
			result = append(result, current.Format("15:04"))
			current = current.Add(time.Minute)
		}
	}
	appendRange(9, 31, 11, 30)
	appendRange(13, 1, 15, 0)
	return result
}

func verify(dbPath, tradeDate string, clocks []string) error {
	db, err := openDB(dbPath, true)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, rankType := range []string{"industry", "concept"} {
		var minutes int
		if err := db.QueryRow(`SELECT COUNT(DISTINCT snapshot_at) FROM rank_intraday_work WHERE trade_date=? AND rank_type=?`, tradeDate, rankType).Scan(&minutes); err != nil {
			return err
		}
		log.Printf("%s distinct minutes = %d", rankType, minutes)
		for _, clock := range clocks {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM rank_intraday_work
WHERE trade_date=? AND rank_type=? AND strftime('%H:%M', snapshot_at, '+8 hours')=?`, tradeDate, rankType, clock).Scan(&count); err != nil {
				return err
			}
			log.Printf("%s rows at %s = %d", rankType, clock, count)
		}
		var missingMinutes, missingResearch string
		if err := db.QueryRow(`SELECT missing_minutes_json,missing_research_json FROM research_quality WHERE trade_date=? AND rank_type=?`, tradeDate, rankType).Scan(&missingMinutes, &missingResearch); err != nil {
			log.Printf("quality %s: %v", rankType, err)
		} else {
			log.Printf("%s quality missing_minutes=%s missing_research=%s", rankType, missingMinutes, missingResearch)
		}
	}
	return nil
}

func newRunSuffix() string {
	var seed [6]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return time.Now().UTC().Format("20060102150405")
	}
	return hex.EncodeToString(seed[:])
}
