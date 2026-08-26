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
	"time"

	"github.com/roiding/shadowflow/internal/datasource/eastmoney"
	"github.com/roiding/shadowflow/internal/graymarket"

	_ "modernc.org/sqlite"
)

const (
	baseURL   = "https://quotederivates.eastmoney.com/datacenter/darktrade"
	tradeDate = "2026-08-25"
	timestamp = time.RFC3339Nano
)

var shanghai = time.FixedZone("CST", 8*3600)

type payload struct {
	Records   []graymarket.RankRecord `json:"records"`
	Points    []graymarket.MoneyPoint `json:"points"`
	Completed int                     `json:"completed"`
	Error     string                  `json:"error,omitempty"`
}

func main() {
	dbPath := flag.String("db", "", "path to shadowflow.db")
	outDir := flag.String("out", "/tmp/stock_backfill", "output directory")
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

// collect reads the stock daily-close identity rows (already persisted by the
// online run) and fetches every stock's 5m darktrade curve from upstream.
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
FROM rank_snapshot
WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type='stock'
ORDER BY rank`, tradeDate)
	if err != nil {
		log.Fatalf("query stock daily close rows: %v", err)
	}
	defer rows.Close()

	snapshotAt := time.Date(2026, 8, 25, 15, 0, 0, 0, shanghai)
	records := make([]graymarket.RankRecord, 0, 5400)
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
		r.RankType = graymarket.RankStock
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
		log.Fatal("no stock daily-close records found in rank_snapshot")
	}
	eligible := 0
	for _, r := range records {
		if r.QuoteAvailable {
			eligible++
		}
	}
	expected := eligible * 48
	log.Printf("loaded %d stock records (%d eligible, expected %d points)", len(records), eligible, expected)

	snapshot := graymarket.RankSnapshot{
		TradeDate:  tradeDate,
		RankType:   graymarket.RankStock,
		SnapshotAt: snapshotAt,
		Records:    records,
	}

	client := eastmoney.NewClient(baseURL, &http.Client{Timeout: 15 * time.Second}, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	var points []graymarket.MoneyPoint
	completed, fetchErr := fetchCurves(ctx, client, snapshot, expected, &points)
	log.Printf("fetched %d/%d curves, %d points (expected %d)", completed, len(records), len(points), expected)
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
	log.Printf("payload written to %s (%.1f MB)", filepath.Join(outDir, "payload.json"), float64(len(data))/1024/1024)
}

// fetchCurves fetches all money curves and re-fetches codes that still miss
// points until the universe is complete.
func fetchCurves(ctx context.Context, client *eastmoney.Client, snapshot graymarket.RankSnapshot, expected int, points *[]graymarket.MoneyPoint) (int, error) {
	emit := func(curve []graymarket.MoneyPoint) error {
		*points = append(*points, curve...)
		return nil
	}
	completed, fetchErr := client.FetchMoney5mIncremental(ctx, snapshot, true, emit)
	for attempt := 0; (fetchErr != nil || len(*points) != expected) && attempt < 8; attempt++ {
		have := make(map[string]int, len(snapshot.Records))
		for _, p := range *points {
			have[p.Code]++
		}
		missing := make([]graymarket.RankRecord, 0, 64)
		for _, r := range snapshot.Records {
			if r.QuoteAvailable && have[r.Code] < 48 {
				missing = append(missing, r)
			}
		}
		if len(missing) == 0 {
			break
		}
		log.Printf("retry pass %d: %d codes missing points", attempt+1, len(missing))
		sub := snapshot
		sub.Records = missing
		n, err := client.FetchMoney5mIncremental(ctx, sub, true, emit)
		completed += n
		if err != nil {
			fetchErr = errors.Join(fetchErr, err)
		} else {
			fetchErr = nil
		}
	}
	if len(*points) != expected && fetchErr == nil {
		fetchErr = fmt.Errorf("point count mismatch: got %d want %d", len(*points), expected)
	}
	return completed, fetchErr
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
	eligible := 0
	for _, r := range p.Records {
		if r.QuoteAvailable {
			eligible++
		}
	}
	expected := eligible * 48
	if len(p.Points) != expected {
		log.Fatalf("point count mismatch: got %d want %d (collection incomplete)", len(p.Points), expected)
	}

	// Compute per-minute money rank in memory (dark_money desc, code asc),
	// mirroring the pre-streaming archive behavior instead of the SQL
	// ROW_NUMBER UPDATE that was pathologically slow at this row count.
	byMinute := make(map[int][]*graymarket.MoneyPoint, 48)
	for i := range p.Points {
		pt := &p.Points[i]
		index, ok := researchMinuteIndex(pt.SnapshotAt)
		if !ok || pt.TradeDate != tradeDate || pt.RankType != graymarket.RankStock {
			log.Fatalf("bad stock money point %s %s %s", pt.Code, pt.TradeDate, pt.SnapshotAt.Format("15:04"))
		}
		byMinute[index] = append(byMinute[index], pt)
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
	log.Printf("computed money ranks for %d minute groups in memory", len(byMinute))

	db := openDB(dbPath, false)
	defer db.Close()
	runID := newRunID()
	now := time.Now().UTC().Format(timestamp)

	insertPoint := `INSERT INTO stock_research_5m
(trade_date,minute_index,market,code,money_rank,dark_money,regular_money,main_money_inflow,money_available)
VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(trade_date,minute_index,market,code) DO UPDATE SET
money_rank=excluded.money_rank,dark_money=excluded.dark_money,regular_money=excluded.regular_money,
main_money_inflow=excluded.main_money_inflow,money_available=1`

	const chunkSize = 24000
	for start := 0; start < len(p.Points); start += chunkSize {
		end := start + chunkSize
		if end > len(p.Points) {
			end = len(p.Points)
		}
		tx, err := db.Begin()
		if err != nil {
			log.Fatalf("begin tx: %v", err)
		}
		if start == 0 {
			for _, q := range []string{
				`DELETE FROM stock_research_5m WHERE trade_date=?`,
				`DELETE FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type='stock'`,
			} {
				if _, err := tx.Exec(q, tradeDate); err != nil {
					tx.Rollback()
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
				if _, err := tx.Exec(insertRecord, runID, r.SnapshotAt.Format(timestamp), r.TradeDate, tradeDate,
					string(graymarket.SnapshotDailyClose), string(r.RankType), r.Rank, r.Market, r.Code, r.Name,
					r.QuoteTime, r.LatestPriceRaw, r.OpenPrice, r.HighPrice, r.LowPrice, r.ClosePrice,
					r.PreviousClose, r.ChangeValue, r.ChangePct, r.Volume, r.Turnover, r.TurnoverRate,
					r.Amplitude, boolInt(r.QuoteAvailable), boolInt(r.MoneyAvailable), r.DarkMoney,
					r.RegularMoney, r.MainMoneyInflow, r.DarkActivity, r.DarkInflowRatio, r.UpCount,
					r.FlatCount, r.DownCount, r.LeaderName, r.LeaderCode, r.SourceVersion,
					r.SourceSortFlag, boolInt(r.SourceDescending), r.FetchedAt.Format(timestamp)); err != nil {
					tx.Rollback()
					log.Fatalf("insert rank_snapshot %s: %v", r.Code, err)
				}
			}
			log.Printf("inserted %d rank_snapshot rows", len(p.Records))
		}
		for _, pt := range p.Points[start:end] {
			minuteIndex, _ := researchMinuteIndex(pt.SnapshotAt)
			if _, err := tx.Exec(insertPoint, pt.TradeDate, minuteIndex, pt.Market, pt.Code, pt.Rank,
				pt.DarkMoney, pt.RegularMoney, pt.MainMoneyInflow, 1); err != nil {
				tx.Rollback()
				log.Fatalf("insert stock_research_5m %s %s: %v", pt.Code, pt.SnapshotAt.Format("15:04"), err)
			}
		}
		if err := tx.Commit(); err != nil {
			log.Fatalf("commit chunk %d: %v", start/chunkSize, err)
		}
		log.Printf("inserted rows %d..%d", start, end)
	}

	tx, err := db.Begin()
	if err != nil {
		log.Fatalf("begin final tx: %v", err)
	}
	defer tx.Rollback()
	if err := upsertStockQuality(tx, eligible, len(p.Points), now); err != nil {
		log.Fatalf("quality: %v", err)
	}
	if err := upsertManifest(tx, now); err != nil {
		log.Fatalf("manifest: %v", err)
	}
	if _, err := tx.Exec(`UPDATE scheduled_job SET status='succeeded',attempt_count=1,max_attempts=1,
retry_at=NULL,lease_owner=NULL,lease_until=NULL,started_at=coalesce(started_at,?),finished_at=?,
last_error_code='',last_error_message='',duration_ms=0
WHERE kind='end-of-day-stock' AND trade_date=? AND status IN ('queued','running','failed')`,
		now, now, tradeDate); err != nil {
		log.Fatalf("update scheduled jobs: %v", err)
	}
	if _, err := tx.Exec(`UPDATE collection_run SET status='failed',error_code='interrupted',
error_message='superseded by manual backfill',finished_at=?
WHERE status='running' AND rank_type='stock' AND snapshot_kind='research_5m' AND started_at>=?`,
		now, "2026-08-25"); err != nil {
		log.Fatalf("update stale collection run: %v", err)
	}
	log.Printf("marked end-of-day-stock scheduled jobs succeeded")
	if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	log.Printf("COMMIT ok, run_id=%s", runID)
}

func upsertStockQuality(tx *sql.Tx, eligible, moneyRows int, now string) error {
	var klineRows int
	if err := tx.QueryRow(`SELECT coalesce(sum(kline_available),0) FROM stock_research_5m WHERE trade_date=?`, tradeDate).Scan(&klineRows); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO stock_archive_quality
(trade_date,expected_stocks,expected_points,expected_kline_stocks,money_rows,kline_rows,daily_close_rows,daily_kline_rows,money_archived_at,updated_at)
VALUES (?,?,48,?,?,?,?,?,?,?)
ON CONFLICT(trade_date) DO UPDATE SET expected_stocks=excluded.expected_stocks,expected_points=48,
expected_kline_stocks=excluded.expected_kline_stocks,money_rows=excluded.money_rows,kline_rows=excluded.kline_rows,
daily_close_rows=excluded.daily_close_rows,daily_kline_rows=excluded.daily_kline_rows,
money_archived_at=excluded.money_archived_at,updated_at=excluded.updated_at`,
		tradeDate, eligible, eligible, moneyRows, klineRows, eligible, eligible, now, now)
	if err != nil {
		return err
	}
	log.Printf("stock quality: expected_stocks=%d money_rows=%d kline_rows=%d", eligible, moneyRows, klineRows)
	return nil
}

func upsertManifest(tx *sql.Tx, now string) error {
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
	q("stock_research_5m rows", `SELECT COUNT(*) FROM stock_research_5m WHERE trade_date=?`, tradeDate)
	q("distinct minute_index", `SELECT COUNT(DISTINCT minute_index) FROM stock_research_5m WHERE trade_date=?`, tradeDate)
	q("rank_snapshot daily_close stock", `SELECT COUNT(*) FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type='stock'`, tradeDate)
	q("money rows for minute 0", `SELECT COUNT(*) FROM stock_research_5m WHERE trade_date=? AND minute_index=0`, tradeDate)
	var mn, mx, zero int
	if err := db.QueryRow(`SELECT MIN(money_rank),MAX(money_rank),SUM(CASE WHEN money_rank<=0 THEN 1 ELSE 0 END) FROM stock_research_5m WHERE trade_date=?`, tradeDate).Scan(&mn, &mx, &zero); err != nil {
		log.Fatalf("rank stats: %v", err)
	}
	log.Printf("money_rank min=%d max=%d zero=%d", mn, mx, zero)
	var incomplete int
	if err := db.QueryRow(`SELECT COUNT(*) FROM (SELECT minute_index FROM stock_research_5m WHERE trade_date=? GROUP BY minute_index HAVING COUNT(*)<>MAX(money_rank))`, tradeDate).Scan(&incomplete); err != nil {
		log.Fatalf("per-minute check: %v", err)
	}
	log.Printf("minute groups with missing ranks = %d", incomplete)
	var saqExpected, saqMoney int
	if err := db.QueryRow(`SELECT expected_stocks,money_rows FROM stock_archive_quality WHERE trade_date=?`, tradeDate).Scan(&saqExpected, &saqMoney); err != nil {
		log.Printf("stock_archive_quality: %v", err)
	} else {
		log.Printf("stock_archive_quality expected=%d money=%d", saqExpected, saqMoney)
	}
	var mstatus string
	if err := db.QueryRow(`SELECT status FROM daily_archive_manifest WHERE trade_date=?`, tradeDate).Scan(&mstatus); err != nil {
		log.Printf("manifest: %v", err)
	} else {
		log.Printf("manifest status=%s", mstatus)
	}
	rows, err := db.Query(`SELECT status,attempt_count FROM scheduled_job WHERE trade_date=? AND kind='end-of-day-stock'`, tradeDate)
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
		log.Printf("scheduled_job end-of-day-stock status=%s attempts=%d", st, ac)
	}
}

func researchMinuteIndex(value time.Time) (int, bool) {
	minutes := value.Hour()*60 + value.Minute()
	switch {
	case minutes >= 9*60+35 && minutes <= 11*60+30 && (minutes-(9*60+35))%5 == 0:
		return (minutes - (9*60 + 35)) / 5, true
	case minutes >= 13*60+5 && minutes <= 15*60 && (minutes-(13*60+5))%5 == 0:
		return 24 + (minutes-(13*60+5))/5, true
	default:
		return 0, false
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
