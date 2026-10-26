package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"golang.org/x/sys/unix"
)

func TestColdIntradayMergePreservesHotCorrections(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	coldPath := filepath.Join(root, "cold.db")
	cold, err := Open(coldPath)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 9, 9, 36, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	save := func(store *Store, stamp time.Time, money int64) {
		t.Helper()
		record := graymarket.RankRecord{TradeDate: "2026-10-09", SnapshotAt: stamp,
			RankType: graymarket.RankIndustry, Code: "BK0448", Name: "bank", Rank: 1,
			DarkMoney: money, MoneyAvailable: true, QuoteAvailable: true, OpenPrice: 12.5, FetchedAt: stamp}
		if err := store.SaveIntraday(ctx, "fixture", graymarket.RankSnapshot{TradeDate: record.TradeDate,
			RankType: record.RankType, SnapshotAt: stamp, Records: []graymarket.RankRecord{record}}, false); err != nil {
			t.Fatal(err)
		}
	}
	save(cold, at, 100)
	save(cold, at.Add(time.Minute), 200)
	if err := cold.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenWithCold(filepath.Join(root, "hot.db"), coldPath, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	points, err := store.IntradaySeries(ctx, graymarket.RankIndustry, "BK0448", "2026-10-09")
	if err != nil || len(points) != 2 || points[0].DarkMoney != 100 || !points[0].MoneyAvailable || points[0].OpenPrice != 12.5 {
		t.Fatalf("cold-only series: %+v, %v", points, err)
	}
	save(store, at, 999)
	if err := store.CleanupArchivedIntraday(ctx, "2027-10-09"); err != nil {
		t.Fatal(err)
	}
	points, err = store.IntradaySeries(ctx, graymarket.RankIndustry, "BK0448", "2026-10-09")
	if err != nil || len(points) != 2 || points[0].DarkMoney != 999 || points[1].DarkMoney != 200 {
		t.Fatalf("merged series: %+v, %v", points, err)
	}
	ranks, err := store.RankAt(ctx, graymarket.RankIndustry, "2026-10-09", at.Add(time.Minute))
	if err != nil || len(ranks) != 1 || ranks[0].DarkMoney != 200 {
		t.Fatalf("cold ranks: %+v, %v", ranks, err)
	}
	if _, err := store.cold.Exec(`DELETE FROM rank_intraday_work`); err == nil {
		t.Fatal("cold connection accepted a write")
	}
	if _, err := store.IntradaySeries(ctx, graymarket.RankConcept, "BK0448", "2026-10-09"); err != nil {
		t.Fatal(err)
	}
	if err := store.cold.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IntradaySeries(ctx, graymarket.RankIndustry, "BK0448", "2026-10-09"); err == nil {
		t.Fatal("cold read failure was silently treated as absent data")
	}
}

func TestColdOpenRejectsMissingSameOrMalformedDatabase(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main.db")
	for _, path := range []string{filepath.Join(root, "missing.db"), mainPath, root} {
		if store, err := OpenWithCold(mainPath, path, 1); err == nil {
			store.Close()
			t.Fatalf("accepted cold path %s", path)
		}
	}
	bad := filepath.Join(root, "bad.db")
	if err := os.WriteFile(bad, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenWithCold(mainPath, bad, 1); err == nil {
		store.Close()
		t.Fatal("accepted corrupt cold database")
	}
	if _, err := os.Stat(filepath.Join(root, "missing.db")); !os.IsNotExist(err) {
		t.Fatal("missing cold database was created")
	}
}

func TestOpenRefusesIncompletePairedRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.db")
	if err := os.WriteFile(path+".restore-in-progress", []byte("interrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(path); err == nil {
		store.Close()
		t.Fatal("service opened a partially restored database pair")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("failed startup created or modified the database")
	}
}

func TestServiceLeaseExcludesRestoreAndAllowsOtherStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
	lease, err := os.OpenFile(path+".service.lock", os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
		t.Fatal("exclusive restore lease acquired while service is using database")
	}
	store.Close()
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(path); err == nil {
		opened.Close()
		t.Fatal("service opened database while restore held exclusive lease")
	}
}

func TestLatestWorkUsesTypeLeadingIndex(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.db.Query(`EXPLAIN QUERY PLAN SELECT max(snapshot_at) FROM rank_intraday_work WHERE rank_type=?`, "industry")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var id, parent, unused int
		var plan string
		if err := rows.Scan(&id, &parent, &unused, &plan); err != nil {
			t.Fatal(err)
		}
		found = found || strings.Contains(plan, "idx_intraday_type_latest (rank_type=?)")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("latest snapshot query did not use a rank-type bounded lookup")
	}
}
