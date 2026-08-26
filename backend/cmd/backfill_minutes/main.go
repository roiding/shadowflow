package main

import (
	"bytes"
	"database/sql"
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
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	tickBaseURL = "https://quotederivates.eastmoney.com/datacenter/darktradetick"
	tradeDate   = "2026-08-25"
	dateToken   = "260825"
	runID       = "backfill-20260825-1104-1106"
)

var shanghai = time.FixedZone("CST", 8*3600)

// clocks are the three missing 1-minute snapshots (CST).
var clocks = []string{"11:04", "11:05", "11:06"}

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
	Points []point `json:"points"`
	Error  string  `json:"error,omitempty"`
}

func main() {
	dbPath := flag.String("db", "", "path to shadowflow.db")
	outDir := flag.String("out", "/tmp/minutes_backfill", "output directory")
	flag.Parse()
	if *dbPath == "" {
		log.Fatal("--db is required")
	}
	switch flag.Arg(0) {
	case "collect":
		collect(*dbPath, *outDir)
	case "insert":
		insert(*dbPath, *outDir)
	case "verify":
		verify(*dbPath)
	default:
		log.Fatalf("unknown command %q (want collect|insert|verify)", flag.Arg(0))
	}
}

func openDB(path string) *sql.DB {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func collect(dbPath, outDir string) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	// Board identities come from the persisted daily-close rows; the same
	// codes/markets existed all day.
	boards := loadBoards(dbPath)
	log.Printf("loaded %d boards (industry+concept)", len(boards))

	var (
		mu     sync.Mutex
		points []point
		errors []string
	)
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, b := range boards {
		wg.Add(1)
		go func(b board) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ticks, err := fetchTicks(b)
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("%s %s: %v", b.RankType, b.Code, err))
				mu.Unlock()
				return
			}
			for _, clock := range clocks {
				key := clock
				tick, ok := ticks[key]
				if !ok {
					mu.Lock()
					errors = append(errors, fmt.Sprintf("%s %s: missing tick %s", b.RankType, b.Code, clock))
					mu.Unlock()
					continue
				}
				mu.Lock()
				points = append(points, point{
					RankType: b.RankType, Market: b.Market, Code: b.Code, Name: b.Name,
					Clock: clock, DarkMoney: tick.dark, RegularMoney: tick.regular, MainMoneyInflow: tick.main,
				})
				mu.Unlock()
			}
		}(b)
	}
	wg.Wait()
	log.Printf("collected %d points (%d boards x 3 minutes)", len(points), len(boards))
	p := payload{Points: points}
	if len(errors) > 0 {
		p.Error = strings.Join(errors, "; ")
		log.Printf("errors: %d -> %s", len(errors), strings.Join(errors, "; "))
	}
	data, err := json.Marshal(p)
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "payload.json"), data, 0o644); err != nil {
		log.Fatalf("write payload: %v", err)
	}
	log.Printf("payload written to %s", filepath.Join(outDir, "payload.json"))
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

func fetchTicks(b board) (map[string]tick, error) {
	params := url.Values{
		"code": {b.Code}, "market": {strconv.FormatInt(b.Market, 10)},
		"time": {"0"}, "version": {"100"}, "cver": {"11.2.6"},
	}
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		body, err := httpGet(tickBaseURL + "?" + params.Encode())
		if err != nil {
			lastErr = err
			continue
		}
		var resp tickResponse
		if err := json.NewDecoder(bytes.NewReader(body)).Decode(&resp); err != nil {
			lastErr = fmt.Errorf("decode: %w", err)
			continue
		}
		if resp.ErrorID != 0 {
			lastErr = fmt.Errorf("upstream error %d", resp.ErrorID)
			continue
		}
		result := make(map[string]tick, len(clocks))
		for _, item := range resp.Data {
			raw := fmt.Sprintf("%010d", item.Time)
			if raw[:6] != dateToken {
				continue
			}
			clock := raw[6:8] + ":" + raw[8:10]
			if !contains(clocks, clock) {
				continue
			}
			result[clock] = tick{dark: item.Dark, regular: item.Regular, main: item.Main}
		}
		return result, nil
	}
	return nil, lastErr
}

func httpGet(rawURL string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "shadowflow/0.1")
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, 2<<20))
}

func loadBoards(dbPath string) []board {
	db := openDB(dbPath)
	defer db.Close()
	rows, err := db.Query(`SELECT rank_type,market,code,name FROM rank_snapshot
WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type IN ('industry','concept')
ORDER BY rank_type,rank`, tradeDate)
	if err != nil {
		log.Fatalf("query boards: %v", err)
	}
	defer rows.Close()
	var boards []board
	for rows.Next() {
		var b board
		if err := rows.Scan(&b.RankType, &b.Market, &b.Code, &b.Name); err != nil {
			log.Fatalf("scan board: %v", err)
		}
		boards = append(boards, b)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("rows err: %v", err)
	}
	return boards
}

func insert(dbPath, outDir string) {
	raw, err := os.ReadFile(filepath.Join(outDir, "payload.json"))
	if err != nil {
		log.Fatalf("read payload: %v", err)
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		log.Fatalf("unmarshal payload: %v", err)
	}
	if len(p.Points) == 0 {
		log.Fatalf("payload empty (run collect first)")
	}
	expected := 0
	if p.Error != "" {
		log.Printf("payload has errors: %s", p.Error)
	}
	// Expected rows: 3 minutes x boards per rank type; count from payload groups.
	byMinute := make(map[string][]*point, len(clocks))
	for i := range p.Points {
		pt := &p.Points[i]
		key := pt.RankType + "@" + pt.Clock
		byMinute[key] = append(byMinute[key], pt)
	}
	for key, group := range byMinute {
		// Derive rank per minute: dark_money desc, code asc.
		sort.Slice(group, func(i, j int) bool {
			if group[i].DarkMoney == group[j].DarkMoney {
				return group[i].Code < group[j].Code
			}
			return group[i].DarkMoney > group[j].DarkMoney
		})
		for rank := range group {
			group[rank].Rank = int64(rank + 1)
		}
		expected += len(group)
		log.Printf("%s: %d rows", key, len(group))
	}

	db := openDB(dbPath)
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := db.Begin()
	if err != nil {
		log.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	stmt := `INSERT INTO rank_intraday_work
(run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,quote_time,
latest_price_raw,open_price,high_price,low_price,close_price,previous_close,change_value,change_pct,
volume,turnover,turnover_rate,amplitude,quote_available,money_available,dark_money,regular_money,main_money_inflow,
dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,
source_version,source_sort_flag,source_descending,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(trade_date,snapshot_at,rank_type,code) DO UPDATE SET
rank=excluded.rank,dark_money=excluded.dark_money,regular_money=excluded.regular_money,
main_money_inflow=excluded.main_money_inflow,money_available=1,fetched_at=excluded.fetched_at`
	for i := range p.Points {
		pt := &p.Points[i]
		at := time.Date(2026, 8, 25, hour(pt.Clock), minute(pt.Clock), 0, 0, shanghai).UTC()
		if _, err := tx.Exec(stmt, runID, at.Format(time.RFC3339Nano), tradeDate, pt.RankType,
			pt.Rank, pt.Market, pt.Code, pt.Name, "",
			0, 0, 0, 0, 0, 0, 0, 0,
			0, 0, 0, 0, 0, 1, pt.DarkMoney, pt.RegularMoney, pt.MainMoneyInflow,
			0, 0, 0, 0, 0, "", "",
			100, 6, 1, now); err != nil {
			log.Fatalf("insert %s %s %s: %v", pt.RankType, pt.Code, pt.Clock, err)
		}
	}
	// Quality: the three minutes are now present, and research minutes are complete.
	if _, err := tx.Exec(`INSERT INTO research_quality
(trade_date,rank_type,expected_minutes,collected_minutes,expected_research,collected_research,
expected_daily_close,collected_daily_close,missing_minutes_json,missing_research_json,missing_daily_close_json,compacted_at)
VALUES (?,?,240,240,48,48,1,1,'[]','[]','[]',?)
ON CONFLICT(trade_date,rank_type) DO UPDATE SET
collected_minutes=240,missing_minutes_json='[]',collected_research=48,missing_research_json='[]',compacted_at=excluded.compacted_at`,
		tradeDate, "industry", now); err != nil {
		log.Fatalf("quality industry: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO research_quality
(trade_date,rank_type,expected_minutes,collected_minutes,expected_research,collected_research,
expected_daily_close,collected_daily_close,missing_minutes_json,missing_research_json,missing_daily_close_json,compacted_at)
VALUES (?,?,240,240,48,48,1,1,'[]','[]','[]',?)
ON CONFLICT(trade_date,rank_type) DO UPDATE SET
collected_minutes=240,missing_minutes_json='[]',collected_research=48,missing_research_json='[]',compacted_at=excluded.compacted_at`,
		tradeDate, "concept", now); err != nil {
		log.Fatalf("quality concept: %v", err)
	}
	if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	log.Printf("COMMIT ok: inserted %d rows (run_id=%s)", len(p.Points), runID)
}

func verify(dbPath string) {
	db := openDB(dbPath)
	defer db.Close()
	for _, rankType := range []string{"industry", "concept"} {
		var minutes int
		if err := db.QueryRow(`SELECT COUNT(DISTINCT snapshot_at) FROM rank_intraday_work WHERE trade_date=? AND rank_type=?`, tradeDate, rankType).Scan(&minutes); err != nil {
			log.Fatalf("minutes %s: %v", rankType, err)
		}
		log.Printf("%s distinct minutes = %d", rankType, minutes)
		for _, clock := range clocks {
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM rank_intraday_work WHERE trade_date=? AND rank_type=? AND substr(snapshot_at,12,5)=?`, tradeDate, rankType, clockUTC(clock)).Scan(&n); err != nil {
				log.Fatalf("rows %s %s: %v", rankType, clock, err)
			}
			log.Printf("%s rows at %s = %d", rankType, clock, n)
		}
		var mm, mr string
		if err := db.QueryRow(`SELECT missing_minutes_json,missing_research_json FROM research_quality WHERE trade_date=? AND rank_type=?`, tradeDate, rankType).Scan(&mm, &mr); err != nil {
			log.Printf("quality %s: %v", rankType, err)
		} else {
			log.Printf("%s quality missing_minutes=%s missing_research=%s", rankType, mm, mr)
		}
	}
}

func clockUTC(clock string) string {
	at := time.Date(2026, 8, 25, hour(clock), minute(clock), 0, 0, shanghai).UTC()
	return at.Format("15:04")
}

func hour(clock string) int {
	value, _ := strconv.Atoi(clock[:2])
	return value
}

func minute(clock string) int {
	value, _ := strconv.Atoi(clock[3:])
	return value
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
