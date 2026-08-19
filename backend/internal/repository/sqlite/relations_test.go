package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

func TestRelationBaselineChangesAndAsOfReconstruction(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	day1 := "2026-08-12"
	day2 := "2026-08-13"
	day3 := "2026-08-14"

	baseline := []graymarket.StockBoardRelation{
		testRelation("000001", "平安银行", "BK001", "银行", graymarket.BoardIndustry, 1),
		testRelation("000001", "平安银行", "BK101", "融资融券", graymarket.BoardConcept, 10),
		testRelation("000002", "万科A", "BK002", "房地产开发", graymarket.BoardIndustry, 2),
	}
	result := applyTestRelationRun(t, store, ctx, "run-1", day1, baseline)
	if !result.BaselineBuilt || result.RelationCount != 3 || result.AddedCount != 0 || result.RemovedCount != 0 {
		t.Fatalf("unexpected baseline result: %+v", result)
	}
	if records, err := store.StockBoardRelations(ctx, "000001", "2026-08-11"); err != nil || len(records) != 0 {
		t.Fatalf("relations must not exist before baseline: records=%+v err=%v", records, err)
	}
	if records, err := store.StockBoardRelations(ctx, "000001", day1); err != nil || len(records) != 2 {
		t.Fatalf("baseline reconstruction failed: records=%+v err=%v", records, err)
	}

	result = applyTestRelationRun(t, store, ctx, "run-2", day2, baseline)
	if result.BaselineBuilt || result.AddedCount != 0 || result.RemovedCount != 0 {
		t.Fatalf("unchanged scan wrote changes: %+v", result)
	}
	changes, err := store.RelationChanges(ctx, day2, "")
	if err != nil || len(changes) != 0 {
		t.Fatalf("unchanged day must have no events: changes=%+v err=%v", changes, err)
	}

	changed := []graymarket.StockBoardRelation{
		testRelation("000001", "平安银行股份", "BK001", "银行", graymarket.BoardIndustry, 1),
		testRelation("000002", "万科A", "BK002", "房地产开发", graymarket.BoardIndustry, 2),
		testRelation("000002", "万科A", "BK102", "深股通", graymarket.BoardConcept, 11),
	}
	result = applyTestRelationRun(t, store, ctx, "run-3", day3, changed)
	if result.AddedCount != 1 || result.RemovedCount != 1 || result.RelationCount != 3 {
		t.Fatalf("unexpected change result: %+v", result)
	}
	changes, err = store.RelationChanges(ctx, day3, "")
	if err != nil || len(changes) != 2 || changes[0].ChangeType != graymarket.RelationAdded || changes[1].ChangeType != graymarket.RelationRemoved {
		t.Fatalf("unexpected persisted changes: changes=%+v err=%v", changes, err)
	}
	day2Boards, err := store.StockBoardRelations(ctx, "000001", day2)
	if err != nil || len(day2Boards) != 2 {
		t.Fatalf("historical state changed after later sync: records=%+v err=%v", day2Boards, err)
	}
	day3Boards, err := store.StockBoardRelations(ctx, "000001", day3)
	if err != nil || len(day3Boards) != 1 || day3Boards[0].BoardCode != "BK001" {
		t.Fatalf("removed relation still exists at day 3: records=%+v err=%v", day3Boards, err)
	}
	constituents, err := store.BoardStockRelations(ctx, graymarket.BoardConcept, "BK102", day3)
	if err != nil || len(constituents) != 1 || constituents[0].StockCode != "000002" {
		t.Fatalf("added board constituent missing: records=%+v err=%v", constituents, err)
	}

	result = applyTestRelationRun(t, store, ctx, "run-4", day3, changed)
	if result.AddedCount != 0 || result.RemovedCount != 0 {
		t.Fatalf("same-day retry was not idempotent: %+v", result)
	}
	changes, err = store.RelationChanges(ctx, day3, "")
	if err != nil || len(changes) != 2 {
		t.Fatalf("same-day retry duplicated events: changes=%+v err=%v", changes, err)
	}
}

func TestRelationHistoryTracksNewBoardsAndIndustryChanges(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	day1, day2 := "2026-08-18", "2026-08-19"
	baseline := []graymarket.StockBoardRelation{
		testRelation("000001", "平安银行", "BK001", "银行", graymarket.BoardIndustry, 1),
		testRelation("000001", "平安银行", "BK101", "融资融券", graymarket.BoardConcept, 1),
	}
	applyTestRelationRun(t, store, ctx, "baseline-new-boards", day1, baseline)

	changed := []graymarket.StockBoardRelation{
		testRelation("000001", "平安银行", "BK003", "跨行业分类", graymarket.BoardIndustry, 1),
		testRelation("000001", "平安银行", "BK101", "融资融券", graymarket.BoardConcept, 1),
		testRelation("000001", "平安银行", "BK103", "新增概念", graymarket.BoardConcept, 2),
	}
	result := applyTestRelationRun(t, store, ctx, "new-boards", day2, changed)
	if result.AddedCount != 2 || result.RemovedCount != 1 {
		t.Fatalf("unexpected relation changes: %+v", result)
	}
	before, err := store.StockBoardRelations(ctx, "000001", day1)
	if err != nil || len(before) != 2 {
		t.Fatalf("original relations were not retained historically: records=%+v err=%v", before, err)
	}
	after, err := store.StockBoardRelations(ctx, "000001", day2)
	if err != nil || len(after) != 3 {
		t.Fatalf("updated relations were not reconstructed: records=%+v err=%v", after, err)
	}
	seen := make(map[string]bool)
	for _, relation := range after {
		seen[string(relation.BoardType)+":"+relation.BoardCode] = true
	}
	if seen["industry:BK001"] || !seen["industry:BK003"] || !seen["concept:BK103"] {
		t.Fatalf("industry replacement or new concept was lost: records=%+v", after)
	}
}

func TestFailedRelationRunDoesNotChangeCurrentState(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	baseline := []graymarket.StockBoardRelation{testRelation("000001", "平安银行", "BK001", "银行", graymarket.BoardIndustry, 1)}
	applyTestRelationRun(t, store, ctx, "baseline", "2026-08-13", baseline)

	startedAt := time.Now().UTC()
	run := repository.RelationSyncRun{RunID: "failed", TradeDate: "2026-08-14", Status: repository.RunRunning, StartedAt: startedAt}
	if err := store.StartRelationSync(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.StageRelations(ctx, run.RunID, []graymarket.StockBoardRelation{testRelation("000002", "万科A", "BK002", "房地产开发", graymarket.BoardIndustry, 2)}); err != nil {
		t.Fatal(err)
	}
	finishedAt := time.Now().UTC()
	run.Status, run.FinishedAt, run.ErrorCode, run.ErrorMessage = repository.RunFailed, &finishedAt, "upstream_error", "test failure"
	if err := store.FailRelationSync(ctx, run); err != nil {
		t.Fatal(err)
	}
	records, err := store.StockBoardRelations(ctx, "000001", "2026-08-14")
	if err != nil || len(records) != 1 {
		t.Fatalf("failed run damaged current history: records=%+v err=%v", records, err)
	}
	if records, err := store.StockBoardRelations(ctx, "000002", "2026-08-14"); err != nil || len(records) != 0 {
		t.Fatalf("failed staged data became visible: records=%+v err=%v", records, err)
	}
	var staged int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM stock_board_relation_stage WHERE run_id='failed'`).Scan(&staged); err != nil || staged != 0 {
		t.Fatalf("failed stage was not cleaned: count=%d err=%v", staged, err)
	}
}

func TestRelationChangesOnBaselineDateAreReplayed(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	tradeDate := "2026-08-14"
	baseline := []graymarket.StockBoardRelation{testRelation("000001", "平安银行", "BK001", "银行", graymarket.BoardIndustry, 1)}
	applyTestRelationRun(t, store, ctx, "initial", tradeDate, baseline)
	changed := []graymarket.StockBoardRelation{testRelation("000001", "平安银行", "BK101", "融资融券", graymarket.BoardConcept, 1)}
	applyTestRelationRun(t, store, ctx, "same-day-change", tradeDate, changed)

	relations, err := store.StockBoardRelations(ctx, "000001", tradeDate)
	if err != nil || len(relations) != 1 || relations[0].BoardCode != "BK101" {
		t.Fatalf("same-day events were not applied after baseline: records=%+v err=%v", relations, err)
	}
}

func applyTestRelationRun(t *testing.T, store *Store, ctx context.Context, runID, tradeDate string, relations []graymarket.StockBoardRelation) repository.RelationApplyResult {
	t.Helper()
	startedAt := time.Now().UTC()
	run := repository.RelationSyncRun{RunID: runID, TradeDate: tradeDate, Status: repository.RunRunning, StartedAt: startedAt}
	if err := store.StartRelationSync(ctx, run); err != nil {
		t.Fatal(err)
	}
	for index := range relations {
		relations[index].DetectedAt = startedAt
	}
	if err := store.StageRelations(ctx, runID, relations); err != nil {
		t.Fatal(err)
	}
	result, err := store.ApplyRelationScan(ctx, runID, tradeDate, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func testRelation(stockCode, stockName, boardCode, boardName string, boardType graymarket.BoardType, order int) graymarket.StockBoardRelation {
	return graymarket.StockBoardRelation{
		StockCode: stockCode, StockName: stockName, BoardCode: boardCode, BoardName: boardName,
		BoardType: boardType, SourceOrder: order, RelationSource: graymarket.RelationSourceQuoteClist,
		RelationScope: graymarket.RelationScopeBoardConstituents, RawData: `{}`,
	}
}
