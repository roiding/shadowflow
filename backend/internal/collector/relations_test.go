package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
)

type fakeRelationSource struct {
	boards       map[graymarket.BoardType][]graymarket.Board
	constituents map[string][]graymarket.StockBoardRelation
	catalogErr   error
}

func (f *fakeRelationSource) FetchAll(context.Context, graymarket.RankType, string, time.Time) (graymarket.RankSnapshot, error) {
	return graymarket.RankSnapshot{}, errors.New("not used")
}

func (f *fakeRelationSource) FetchBoardCatalog(_ context.Context, boardType graymarket.BoardType) ([]graymarket.Board, error) {
	if f.catalogErr != nil {
		return nil, f.catalogErr
	}
	return f.boards[boardType], nil
}

func (f *fakeRelationSource) FetchBoardConstituents(_ context.Context, board graymarket.Board) ([]graymarket.StockBoardRelation, error) {
	return f.constituents[board.Code], nil
}

func TestCollectStockBoardRelationsBuildsBaselineAndChanges(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := &fakeRelationSource{
		boards: map[graymarket.BoardType][]graymarket.Board{
			graymarket.BoardIndustry: {{Code: "BK001", Name: "银行", Type: graymarket.BoardIndustry, SourceRank: 1}},
			graymarket.BoardConcept:  {{Code: "BK101", Name: "融资融券", Type: graymarket.BoardConcept, SourceRank: 1}},
		},
		constituents: map[string][]graymarket.StockBoardRelation{
			"BK001": {collectorTestRelation("000001", "BK001", graymarket.BoardIndustry)},
			"BK101": {collectorTestRelation("000001", "BK101", graymarket.BoardConcept)},
		},
	}
	service := New(source, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.retries = 0
	ctx := context.Background()
	if err := service.CollectStockBoardRelations(ctx, "2026-08-13"); err != nil {
		t.Fatal(err)
	}
	if !service.HasStockBoardRelations(ctx, "2026-08-13") {
		t.Fatal("successful relation sync was not recorded")
	}
	relations, err := store.StockBoardRelations(ctx, "000001", "2026-08-13")
	if err != nil || len(relations) != 2 {
		t.Fatalf("baseline was not queryable: records=%+v err=%v", relations, err)
	}
	source.constituents["BK101"] = nil
	if err := service.CollectStockBoardRelations(ctx, "2026-08-14"); err != nil {
		t.Fatal(err)
	}
	changes, err := store.RelationChanges(ctx, "2026-08-14", graymarket.BoardConcept)
	if err != nil || len(changes) != 1 || changes[0].ChangeType != graymarket.RelationRemoved {
		t.Fatalf("removal was not recorded: changes=%+v err=%v", changes, err)
	}
}

func TestCollectStockBoardRelationsDoesNotApplyPartialScan(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := &fakeRelationSource{catalogErr: errors.New("upstream unavailable")}
	service := New(source, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.retries = 0
	err = service.CollectStockBoardRelations(context.Background(), "2026-08-14")
	if err == nil {
		t.Fatal("expected relation sync failure")
	}
	if service.HasStockBoardRelations(context.Background(), "2026-08-14") {
		t.Fatal("failed relation sync was marked successful")
	}
}

func collectorTestRelation(stockCode, boardCode string, boardType graymarket.BoardType) graymarket.StockBoardRelation {
	return graymarket.StockBoardRelation{
		StockCode: stockCode, StockName: "stock", BoardCode: boardCode, BoardName: "board", BoardType: boardType,
		RelationSource: graymarket.RelationSourceQuoteClist, RelationScope: graymarket.RelationScopeBoardConstituents,
		DetectedAt: time.Now().UTC(), RawData: `{}`,
	}
}
