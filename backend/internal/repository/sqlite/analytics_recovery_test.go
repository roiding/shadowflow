package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

func TestSealRetriesAfterKlinesCommittedAndAnalyticsFailed(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	date := "2026-09-04"
	completeArchiveFixture(t, store, date, 1, false)
	if complete, err := store.HasStockKlineArchive(ctx, date); err != nil || !complete {
		t.Fatalf("kline fixture incomplete: %v %v", complete, err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_features BEFORE INSERT ON daily_feature BEGIN SELECT RAISE(ABORT,'feature failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SealArchiveRevision(ctx, date, "attempt-one"); err == nil {
		t.Fatal("expected first seal failure")
	}
	revisions, err := store.ArchiveRevisions(ctx, date)
	if err != nil || len(revisions) != 0 {
		t.Fatalf("failed seal partially committed: %v %v", revisions, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_features`); err != nil {
		t.Fatal(err)
	}
	first, err := store.SealArchiveRevision(ctx, date, "attempt-two")
	if err != nil {
		t.Fatal(err)
	}
	features, _, err := store.DailyFeatures(ctx, date, "", graymarket.RankStock)
	if err != nil || len(features) != 1 {
		t.Fatalf("retry did not generate analytics: %v %v", features, err)
	}
	second, err := store.SealArchiveRevision(ctx, date, "attempt-three")
	if err != nil || second != first {
		t.Fatalf("unchanged seal was not idempotent: %v %v %v", first, second, err)
	}
	refreshedAt := "2026-09-07T01:02:03Z"
	if _, err := store.db.Exec(`UPDATE daily_archive_manifest SET updated_at=? WHERE trade_date=?`, refreshedAt, date); err != nil {
		t.Fatal(err)
	}
	third, err := store.SealArchiveRevision(ctx, date, "attempt-four")
	if err != nil || third != first {
		t.Fatalf("manifest-only refresh changed revision: %v %v", third, err)
	}
	var saved string
	if err := store.db.QueryRow(`SELECT manifest_json FROM daily_archive_revision WHERE revision_id=?`, first.RevisionID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	var manifest repository.DailyArchiveManifest
	if err := json.Unmarshal([]byte(saved), &manifest); err != nil || manifest.UpdatedAt == nil || formatTimestamp(*manifest.UpdatedAt) != refreshedAt {
		t.Fatalf("seal still references old manifest: %s %v", saved, err)
	}
}

func TestMiddleDateBackfillReplacesLabelTargetsAndFutureWindows(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	completeArchiveFixture(t, store, "2026-09-02", 1, true)
	completeArchiveFixture(t, store, "2026-09-04", 1, true)
	completeArchiveFixture(t, store, "2026-09-03", 1, true)
	labels, err := store.FutureReturnLabels(ctx, "2026-09-02", "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	stocks := 0
	for _, label := range labels {
		if label.RankType != graymarket.RankStock {
			continue
		}
		stocks++
		if label.TargetDate != "2026-09-03" {
			t.Fatalf("stale horizon target retained: %v", label)
		}
	}
	if stocks != 1 {
		t.Fatalf("expected one current stock label, got %d", stocks)
	}
	features, set, err := store.DailyFeatures(ctx, "2026-09-04", "", graymarket.RankStock)
	if err != nil || len(features) != 1 || features[0].ConsecutiveInflowDays != 3 || len(set.SourceRevisions) != 3 {
		t.Fatalf("later window was not rebuilt: features=%v sources=%v err=%v", features, set.SourceRevisions, err)
	}
}

func TestHistoricalCorrectionRefreshesFutureFeaturesWithSameRevisionID(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	snapshot := completeArchiveFixture(t, store, "2026-09-03", 1, true)
	completeArchiveFixture(t, store, "2026-09-04", 1, true)
	snapshot.Records[0].MainMoneyInflow = -150
	snapshot.Records[0].DarkMoney = -100
	snapshot.Records[0].RegularMoney = -50
	if err := store.SaveStockArchive(ctx, "correction", snapshot, testMoneyPoints(snapshot)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStockKlines(ctx, "correction", testStockKlines(snapshot.TradeDate, snapshot.SnapshotAt.Location(), snapshot.Records[0])); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SealArchiveRevision(ctx, snapshot.TradeDate, "fixture-"+snapshot.TradeDate); err != nil {
		t.Fatal(err)
	}
	features, _, err := store.DailyFeatures(ctx, "2026-09-04", "", graymarket.RankStock)
	if err != nil || len(features) != 1 || features[0].ConsecutiveInflowDays != 1 {
		t.Fatalf("historical correction did not propagate: %v %v", features, err)
	}
}
