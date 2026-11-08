package collector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/repository/sqlite"
	"github.com/roiding/shadowflow/internal/thsfocus"
)

type fixedFocusSource struct {
	snapshot thsfocus.Snapshot
	err      error
}

func (source *fixedFocusSource) FetchDay(context.Context, string, string) (thsfocus.Snapshot, error) {
	return source.snapshot, source.err
}

func TestThsFocusFailurePreservesPublishedSupplement(testingT *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		testingT.Fatal(err)
	}
	defer store.Close()
	runAt := time.Date(2026, 11, 6, 16, 0, 0, 0, thsfocus.Shanghai())
	source := &fixedFocusSource{snapshot: thsfocus.Snapshot{
		TradeDate: "2026-11-06", Source: thsfocus.Source, Scope: thsfocus.ScopeDefault, MarketScope: thsfocus.MarketScope,
		FetchedAt: runAt, Blocks: []thsfocus.Block{}, Ladder: []thsfocus.Level{}, Limitations: []string{"源辅助数据"},
		Market:     json.RawMessage(`{"rise_fall":{"rise":100,"fall":200,"deuce":10,"limit_up":10,"limit_down":2}}`),
		Comparison: json.RawMessage(`{"today_date":"2026-11-06","yesterday_date":"2026-11-05","board_list":[],"one_board":[]}`),
		RawPages:   []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)},
	}}
	service := New(nil, store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithThsFocusSource(source, thsfocus.ScopeDefault)
	if err := service.CollectThsFocus(context.Background(), runAt); err != nil {
		testingT.Fatal(err)
	}
	source.err = errors.New("source failed")
	if err := service.CollectThsFocus(context.Background(), runAt); err == nil {
		testingT.Fatal("source failure was swallowed")
	}
	complete, err := service.HasThsFocusArchive(context.Background(), "2026-11-06")
	if err != nil || !complete {
		testingT.Fatalf("failed source removed prior data: %v", err)
	}
	source.err = nil
	source.snapshot.TradeDate = "2026-11-05"
	if err := service.CollectThsFocus(context.Background(), runAt); err == nil {
		testingT.Fatal("wrong response date published")
	}
}
