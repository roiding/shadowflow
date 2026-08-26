package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/roiding/shadowflow/internal/datasource/eastmoney"
	"github.com/roiding/shadowflow/internal/graymarket"

	_ "modernc.org/sqlite"
)

const (
	baseURL     = "https://quotederivates.eastmoney.com/datacenter/darktrade"
	tradeDate   = "2026-08-25"
	requestedAt = "20260825"
	timestamp   = time.RFC3339Nano
)

var shanghai = time.FixedZone("CST", 8*3600)

type payload struct {
	Records   []graymarket.RankRecord `json:"records"`
	Points    []graymarket.MoneyPoint `json:"points"`
	Completed int                     `json:"completed"`
	Error     string                  `json:"error,omitempty"`
}


func verify(dbPath string) {
	db := openDB(dbPath, false)
	defer db.Close()
	q := func(label, sql string, args ...any) {
		var n int
		if err := db.QueryRow(sql, args...).Scan(&n); err != nil {
			log.Fatalf("%s: %v", label, err)
		}
		log.Printf("%s = %d", label, n)
	}
	q("concept board_money_5m rows", `SELECT COUNT(*) FROM board_money_5m WHERE trade_date=? AND rank_type='concept'`, tradeDate)
	q("concept rank_snapshot rows", `SELECT COUNT(*) FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type='concept'`, tradeDate)
	q("concept distinct snapshot_at", `SELECT COUNT(DISTINCT snapshot_at) FROM board_money_5m WHERE trade_date=? AND rank_type='concept'`, tradeDate)
	var mn, mx, zero int
	if err := db.QueryRow(`SELECT MIN(rank),MAX(rank),SUM(CASE WHEN rank=0 THEN 1 ELSE 0 END) FROM board_money_5m WHERE trade_date=? AND rank_type='concept'`, tradeDate).Scan(&mn, &mx, &zero); err != nil {
		log.Fatalf("rank stats: %v", err)
	}
	log.Printf("concept rank min=%d max=%d zero=%d", mn, mx, zero)
	var mm, mr string
	if err := db.QueryRow(`SELECT missing_minutes_json,missing_research_json FROM research_quality WHERE trade_date=? AND rank_type='concept'`, tradeDate).Scan(&mm, &mr); err != nil {
		log.Printf("research_quality: %v", err)
	} else {
		log.Printf("research_quality missing_minutes=%s missing_research=%s", mm, mr)
	}
	var mstatus string
	if err := db.QueryRow(`SELECT status FROM daily_archive_manifest WHERE trade_date=?`, tradeDate).Scan(&mstatus); err != nil {
		log.Printf("manifest: %v", err)
	} else {
		log.Printf("manifest status=%s", mstatus)
	}
	rows, err := db.Query(`SELECT status,attempt_count FROM scheduled_job WHERE trade_date=? AND kind='end-of-day-concept'`, tradeDate)
	if err != nil {
		log.Fatalf("scheduled_job: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var ac int
		if err := rows.Scan(&st, &ac); err != nil {
			log.Fatalf("scan job: %v", err)
		}
		log.Printf("scheduled_job end-of-day-concept status=%s attempts=%d", st, ac)
	}
}

func main() {
	dbPath := flag.String("db", "", "path to shadowflow.db")
	outDir := flag.String("out", "/tmp/concept_backfill", "output directory")
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
		log.Fatalf("unknown command %q (want collect|insert)", flag.Arg(0))
	}
}

func newRunID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		log.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(buf)
}

func openDB(path string, readOnly bool) *sql.DB {
	dsn := "file:" + path
	if readOnly {
		dsn += "?mode=ro"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

// collect reads the latest concept intraday snapshot (quote-enriched records)
// from the live DB, fetches every concept's 5m darktrade curve from upstream,
// and stores both on disk. Safe to run while the server is up.
func collect(dbPath, outDir string) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	db := openDB(dbPath, true)
	defer db.Close()

	rows, err := db.Query(`SELECT rank,market,code,name,quote_time,latest_price_raw,
open_price,high_price,low_price,close_price,previous_close,change_value,change_pct,
volume,turnover,turnover_rate,amplitude,quote_available,money_available,dark_money,
regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,up_count,flat_count,
down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at
FROM rank_intraday_work
WHERE trade_date=? AND rank_type='concept'
AND snapshot_at=(SELECT MAX(snapshot_at) FROM rank_intraday_work WHERE trade_date=? AND rank_type='concept')
ORDER BY rank`, tradeDate, tradeDate)
	if err != nil {
		log.Fatalf("query intraday records: %v", err)
	}
	defer rows.Close()

	snapshotAt := time.Date(2026, 8, 25, 15, 0, 0, 0, shanghai)
	records := make([]graymarket.RankRecord, 0, 342)
	for rows.Next() {
		var r graymarket.RankRecord
		var quoteAvail, moneyAvail, descending int
		var fetchedAt string
		err := rows.Scan(&r.Rank, &r.Market, &r.Code, &r.Name, &r.QuoteTime, &r.LatestPriceRaw,
			&r.OpenPrice, &r.HighPrice, &r.LowPrice, &r.ClosePrice, &r.PreviousClose,
			&r.ChangeValue, &r.ChangePct, &r.Volume, &r.Turnover, &r.TurnoverRate,
			&r.Amplitude, &quoteAvail, &moneyAvail, &r.DarkMoney, &r.RegularMoney,
			&r.MainMoneyInflow, &r.DarkActivity, &r.DarkInflowRatio, &r.UpCount,
			&r.FlatCount, &r.DownCount, &r.LeaderName, &r.LeaderCode, &r.SourceVersion,
			&r.SourceSortFlag, &descending, &fetchedAt)
		if err != nil {
			log.Fatalf("scan record: %v", err)
		}
		r.TradeDate = tradeDate
		r.SnapshotAt = snapshotAt
		r.RankType = graymarket.RankConcept
		r.QuoteAvailable = quoteAvail == 1
		r.MoneyAvailable = moneyAvail == 1
		r.SourceDescending = descending == 1
		if t, perr := time.Parse(time.RFC3339Nano, fetchedAt); perr == nil {
			r.FetchedAt = t
		} else if t2, perr2 := time.Parse("2006-01-02 15:04:05", fetchedAt); perr2 == nil {
			r.FetchedAt = t2
		} else {
			r.FetchedAt = time.Now().UTC()
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("rows err: %v", err)
	}
	if len(records) == 0 {
		log.Fatal("no concept intraday records found")
	}
	log.Printf("loaded %d concept records from intraday work", len(records))

	snapshot := graymarket.RankSnapshot{
		TradeDate:  tradeDate,
		RankType:   graymarket.RankConcept,
		SnapshotAt: snapshotAt,
		Records:    records,
	}

	client := eastmoney.NewClient(baseURL, &http.Client{Timeout: 15 * time.Second}, 100)
	var points []graymarket.MoneyPoint
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	completed, fetchErr := client.FetchMoney5mIncremental(ctx, snapshot, true, func(curve []graymarket.MoneyPoint) error {
		points = append(points, curve...)
		return nil
	})
	log.Printf("fetched %d/%d curves, %d points", completed, len(records), len(points))
	if fetchErr != nil {
		log.Printf("fetch error: %v", fetchErr)
	}

	p := payload{Records: records, Points: points, Completed: completed}
	if fetchErr != nil {
		p.Error = fetchErr.Error()
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

func insert(dbPath, outDir string) {
	raw, err := os.ReadFile(filepath.Join(outDir, "payload.json"))
	if err != nil {
		log.Fatalf("read payload: %v", err)
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		log.Fatalf("unmarshal payload: %v", err)
	}
	if len(p.Records) == 0 || len(p.Points) == 0 {
		log.Fatalf("payload empty: records=%d points=%d (run collect first)", len(p.Records), len(p.Points))
	}
	expected := len(p.Records) * 48
	if len(p.Points) != expected {
		log.Fatalf("point count mismatch: got %d want %d (collection incomplete)", len(p.Points), expected)
	}

	db := openDB(dbPath, false)
	defer db.Close()

	runID := newRunID()
	now := time.Now().UTC().Format(timestamp)
	tx, err := db.Begin()
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM board_money_5m WHERE trade_date=? AND rank_type='concept'`,
		`DELETE FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type='concept'`,
		`DELETE FROM raw_response WHERE substr(snapshot_at,1,10)=? AND snapshot_kind='daily_close' AND rank_type='concept'`,
	} {
		if _, err := tx.Exec(q, tradeDate); err != nil {
			log.Fatalf("delete: %v", err)
		}
	}

	insertRecord := `INSERT OR REPLACE INTO rank_snapshot
(run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,
latest_price_raw,open_price,high_price,low_price,close_price,previous_close,change_value,change_pct,
volume,turnover,turnover_rate,amplitude,quote_available,money_available,dark_money,regular_money,main_money_inflow,
dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,
source_version,source_sort_flag,source_descending,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	for _, r := range p.Records {
		_, err := tx.Exec(insertRecord, runID, r.SnapshotAt.Format(timestamp), r.TradeDate, tradeDate,
			string(graymarket.SnapshotDailyClose), string(r.RankType), r.Rank, r.Market, r.Code, r.Name,
			r.QuoteTime, r.LatestPriceRaw, r.OpenPrice, r.HighPrice, r.LowPrice, r.ClosePrice,
			r.PreviousClose, r.ChangeValue, r.ChangePct, r.Volume, r.Turnover, r.TurnoverRate,
			r.Amplitude, boolInt(r.QuoteAvailable), boolInt(r.MoneyAvailable), r.DarkMoney,
			r.RegularMoney, r.MainMoneyInflow, r.DarkActivity, r.DarkInflowRatio, r.UpCount,
			r.FlatCount, r.DownCount, r.LeaderName, r.LeaderCode, r.SourceVersion,
			r.SourceSortFlag, boolInt(r.SourceDescending), r.FetchedAt.Format(timestamp))
		if err != nil {
			log.Fatalf("insert rank_snapshot %s: %v", r.Code, err)
		}
	}
	log.Printf("inserted %d rank_snapshot rows", len(p.Records))

	// Compute per-snapshot money rank in Go (the SQL ROW_NUMBER UPDATE is
	// pathologically slow on this database; darktrade rank comes from the board).
	{
		byMinute := make(map[time.Time][]*graymarket.MoneyPoint)
		for i := range p.Points {
			pt := &p.Points[i]
			byMinute[pt.SnapshotAt] = append(byMinute[pt.SnapshotAt], pt)
		}
		for _, group := range byMinute {
			sort.Slice(group, func(i, j int) bool {
				if group[i].DarkMoney == group[j].DarkMoney {
					return group[i].Code < group[j].Code
				}
				return group[i].DarkMoney > group[j].DarkMoney
			})
			for rank := range group {
				group[rank].Rank = int64(rank + 1)
			}
		}
		log.Printf("computed money ranks for %d snapshot groups in memory", len(byMinute))
	}

	insertPoint := `INSERT INTO board_money_5m
(run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,dark_money,regular_money,main_money_inflow,money_available,source_time,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(trade_date,snapshot_at,rank_type,code) DO UPDATE SET
dark_money=excluded.dark_money,regular_money=excluded.regular_money,main_money_inflow=excluded.main_money_inflow,
money_available=1,source_time=excluded.source_time,fetched_at=excluded.fetched_at`
	for _, pt := range p.Points {
		if pt.TradeDate != tradeDate || pt.RankType != graymarket.RankConcept {
			log.Fatalf("bad point: %s %s %s", pt.TradeDate, pt.RankType, pt.Code)
		}
		_, err := tx.Exec(insertPoint, runID, pt.SnapshotAt.Format(timestamp), pt.TradeDate,
			string(pt.RankType), pt.Rank, pt.Market, pt.Code, pt.Name, pt.DarkMoney,
			pt.RegularMoney, pt.MainMoneyInflow, 1, pt.SourceTime, pt.FetchedAt.Format(timestamp))
		if err != nil {
			log.Fatalf("insert board_money_5m %s %s: %v", pt.Code, pt.SnapshotAt.Format("15:04"), err)
		}
	}
	log.Printf("inserted %d board_money_5m rows (ranks precomputed)", len(p.Points))

	if err := upsertConceptQuality(tx, runID, now); err != nil {
		log.Fatalf("quality: %v", err)
	}
	if err := upsertManifest(tx, now); err != nil {
		log.Fatalf("manifest: %v", err)
	}

	if _, err := tx.Exec(`UPDATE scheduled_job SET status='succeeded',attempt_count=1,max_attempts=1,
retry_at=NULL,lease_owner=NULL,lease_until=NULL,started_at=coalesce(started_at,?),finished_at=?,
last_error_code='',last_error_message='',duration_ms=0
WHERE kind='end-of-day-concept' AND trade_date=? AND status IN ('queued','running','failed')`,
		now, now, tradeDate); err != nil {
		log.Fatalf("update scheduled jobs: %v", err)
	}
	log.Printf("marked end-of-day-concept scheduled jobs succeeded")

	if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	log.Printf("COMMIT ok, run_id=%s", runID)
}

func upsertConceptQuality(tx *sql.Tx, runID, now string) error {
	seen := make(map[string]struct{})
	rows, err := tx.Query(`SELECT DISTINCT snapshot_at FROM rank_intraday_work WHERE trade_date=? AND rank_type='concept'`, tradeDate)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var t time.Time
		if parsed, perr := time.Parse(time.RFC3339Nano, raw); perr == nil {
			t = parsed
		} else if parsed, perr := time.Parse("2006-01-02 15:04:05", raw); perr == nil {
			t = parsed
		} else {
			rows.Close()
			return fmt.Errorf("parse snapshot_at %q: %v", raw, err)
		}
		seen[t.In(shanghai).Format("15:04")] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	minutes := make([]string, 0, len(seen))
	for m := range seen {
		minutes = append(minutes, m)
	}
	sort.Strings(minutes)
	missingMin := missing(expectedMinuteTimes(), minutes)
	researchMin := filterResearchMinutes(minutes)
	missingResearch := missing(expectedResearchTimes(), researchMin)
	missingJSON, _ := json.Marshal(missingMin)
	missingResearchJSON, _ := json.Marshal(missingResearch)
	_, err = tx.Exec(`INSERT OR REPLACE INTO research_quality
(trade_date,rank_type,expected_minutes,collected_minutes,expected_research,collected_research,
expected_daily_close,collected_daily_close,missing_minutes_json,missing_research_json,missing_daily_close_json,compacted_at)
VALUES (?,?,240,?,48,?,1,1,?,?,'[]',?)`,
		tradeDate, string(graymarket.RankConcept), len(minutes), len(researchMin),
		string(missingJSON), string(missingResearchJSON), now)
	log.Printf("concept quality: minutes=%d research=%d missing_minutes=%v missing_research=%v", len(minutes), len(researchMin), missingMin, missingResearch)
	return err
}

func upsertManifest(tx *sql.Tx, now string) error {
	// Mirror refreshArchiveManifest for the board parts; stock parts are
	// whatever the day already has.
	var indClose, conClose, stockClose, stockDailyKline int
	if err := tx.QueryRow(`SELECT
coalesce(sum(CASE WHEN rank_type='industry' THEN 1 ELSE 0 END),0),
coalesce(sum(CASE WHEN rank_type='concept' THEN 1 ELSE 0 END),0),
coalesce(sum(CASE WHEN rank_type='stock' THEN 1 ELSE 0 END),0),
coalesce(sum(CASE WHEN rank_type='stock' AND quote_available=1 THEN 1 ELSE 0 END),0)
FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close'`, tradeDate).
		Scan(&indClose, &conClose, &stockClose, &stockDailyKline); err != nil {
		return err
	}
	var indMoney, conMoney int
	if err := tx.QueryRow(`SELECT
coalesce(sum(CASE WHEN rank_type='industry' THEN 1 ELSE 0 END),0),
coalesce(sum(CASE WHEN rank_type='concept' THEN 1 ELSE 0 END),0)
FROM board_money_5m WHERE trade_date=?`, tradeDate).Scan(&indMoney, &conMoney); err != nil {
		return err
	}
	var stockMoney, stockKline int
	if err := tx.QueryRow(`SELECT coalesce(sum(money_available),0),coalesce(sum(kline_available),0)
FROM stock_research_5m WHERE trade_date=?`, tradeDate).Scan(&stockMoney, &stockKline); err != nil {
		return err
	}
	var expectedStock, expectedKlineStocks, expectedPoints int
	err := tx.QueryRow(`SELECT expected_stocks,expected_kline_stocks,expected_points FROM stock_archive_quality WHERE trade_date=?`, tradeDate).
		Scan(&expectedStock, &expectedKlineStocks, &expectedPoints)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	status := "complete"
	var validation []string
	if indClose == 0 {
		status = "incomplete"
		validation = append(validation, "industry daily close is missing")
	} else if indMoney != indClose*48 {
		status = "incomplete"
		validation = append(validation, fmt.Sprintf("industry money rows: expected %d, got %d", indClose*48, indMoney))
	}
	if conClose == 0 {
		status = "incomplete"
		validation = append(validation, "concept daily close is missing")
	} else if conMoney != conClose*48 {
		status = "incomplete"
		validation = append(validation, fmt.Sprintf("concept money rows: expected %d, got %d", conClose*48, conMoney))
	}
	if expectedStock == 0 {
		status = "incomplete"
		validation = append(validation, "stock archive quality is missing")
	} else {
		if stockClose != expectedStock {
			status = "incomplete"
			validation = append(validation, fmt.Sprintf("stock daily close rows: expected %d, got %d", expectedStock, stockClose))
		}
		if stockMoney != expectedKlineStocks*expectedPoints {
			status = "incomplete"
			validation = append(validation, fmt.Sprintf("stock money rows: expected %d, got %d", expectedKlineStocks*expectedPoints, stockMoney))
		}
		if stockDailyKline != expectedKlineStocks {
			status = "incomplete"
			validation = append(validation, fmt.Sprintf("stock daily kline rows: expected %d, got %d", expectedKlineStocks, stockDailyKline))
		}
		if stockKline != expectedKlineStocks*expectedPoints {
			status = "incomplete"
			validation = append(validation, fmt.Sprintf("stock five-minute kline rows: expected %d, got %d", expectedKlineStocks*expectedPoints, stockKline))
		}
	}
	validationJSON, _ := json.Marshal(validation)
	var completedAt any
	if status == "complete" {
		completedAt = now
	}
	_, err = tx.Exec(`INSERT INTO daily_archive_manifest
(trade_date,status,industry_close_rows,industry_money_rows,concept_close_rows,concept_money_rows,
stock_close_rows,stock_money_rows,stock_kline_rows,stock_daily_kline_rows,
expected_stock_rows,expected_stock_kline_rows,code_count,code_set_sha256,kline_source_counts_json,
darktrade_contract,darktradetick_contract,stock_kline_contract,parser_version,
validation_errors_json,completed_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,0,'','{}','darktrade:version=101,cver=100,sortflag=6,desc=1',
'darktradetick:version=100,cver=11.2.6,points=48','stock-kline:klt=5,fqt=0|trends2:241-to-48-v1',
'shadowflow-archive-v1',?,?,?)
ON CONFLICT(trade_date) DO UPDATE SET status=excluded.status,
industry_close_rows=excluded.industry_close_rows,industry_money_rows=excluded.industry_money_rows,
concept_close_rows=excluded.concept_close_rows,concept_money_rows=excluded.concept_money_rows,
stock_close_rows=excluded.stock_close_rows,stock_money_rows=excluded.stock_money_rows,
stock_kline_rows=excluded.stock_kline_rows,stock_daily_kline_rows=excluded.stock_daily_kline_rows,
expected_stock_rows=excluded.expected_stock_rows,expected_stock_kline_rows=excluded.expected_stock_kline_rows,
validation_errors_json=excluded.validation_errors_json,
completed_at=CASE WHEN excluded.status='complete' THEN coalesce(daily_archive_manifest.completed_at,excluded.completed_at) ELSE NULL END,
updated_at=excluded.updated_at`,
		tradeDate, status, indClose, indMoney, conClose, conMoney, stockClose, stockMoney,
		stockKline, stockDailyKline, expectedStock, expectedKlineStocks*expectedPoints,
		string(validationJSON), completedAt, now)
	return err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func expectedMinuteTimes() []string {
	return timeRange(9, 31, 11, 30, 1, timeRange(13, 1, 15, 0, 1, nil))
}

func expectedResearchTimes() []string {
	return timeRange(9, 35, 11, 30, 5, timeRange(13, 5, 15, 0, 5, nil))
}

func timeRange(startHour, startMinute, endHour, endMinute, step int, tail []string) []string {
	base := time.Date(2000, 1, 1, startHour, startMinute, 0, 0, time.UTC)
	end := time.Date(2000, 1, 1, endHour, endMinute, 0, 0, time.UTC)
	result := make([]string, 0, int(end.Sub(base)/time.Minute)+len(tail)+1)
	for current := base; !current.After(end); current = current.Add(time.Duration(step) * time.Minute) {
		result = append(result, current.Format("15:04"))
	}
	return append(result, tail...)
}

func missing(expected, actual []string) []string {
	seen := make(map[string]struct{}, len(actual))
	for _, v := range actual {
		seen[v] = struct{}{}
	}
	var result []string
	for _, v := range expected {
		if _, ok := seen[v]; !ok {
			result = append(result, v)
		}
	}
	return result
}

func filterResearchMinutes(minutes []string) []string {
	var result []string
	for _, m := range minutes {
		if isResearchMinute(m) {
			result = append(result, m)
		}
	}
	return result
}

func isResearchMinute(value string) bool {
	if len(value) != 5 || !((value >= "09:35" && value <= "11:30") || (value >= "13:05" && value <= "15:00")) {
		return false
	}
	return strings.HasSuffix(value, ":00") || strings.HasSuffix(value, ":05") ||
		strings.HasSuffix(value, ":10") || strings.HasSuffix(value, ":15") ||
		strings.HasSuffix(value, ":20") || strings.HasSuffix(value, ":25") ||
		strings.HasSuffix(value, ":30") || strings.HasSuffix(value, ":35") ||
		strings.HasSuffix(value, ":40") || strings.HasSuffix(value, ":45") ||
		strings.HasSuffix(value, ":50") || strings.HasSuffix(value, ":55")
}
