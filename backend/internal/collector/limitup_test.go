package collector

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/limitup"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
)

type fixedLimitUpSource struct {
	snapshot limitup.Snapshot
	err      error
	calls    int
	date     string
	scope    string
}

func (source *fixedLimitUpSource) FetchDay(_ context.Context, date, scope string) (limitup.Snapshot, error) {
	source.calls++
	source.date, source.scope = date, scope
	return source.snapshot, source.err
}

func TestLimitUpEventArchiveDoesNotDependOnMarketSource(testingT *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		testingT.Fatal(err)
	}
	defer store.Close()
	runAt := time.Date(2026, 11, 5, 16, 0, 0, 0, limitup.Shanghai())
	snapshot := limitup.Snapshot{
		TradeDate: "2026-11-05", Source: limitup.SourceTHS, Scope: limitup.ScopeDefault,
		FetchedAt: runAt, RawPages: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)},
	}
	source := &fixedLimitUpSource{snapshot: snapshot}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := New(nil, store, logger).WithLimitUpSource(source, limitup.ScopeDefault)

	if err := service.CollectLimitUpEvents(context.Background(), runAt); err != nil {
		testingT.Fatalf("event archive unexpectedly required the market source: %v", err)
	}
	if source.calls != 1 || source.date != "2026-11-05" || source.scope != limitup.ScopeDefault {
		testingT.Fatalf("unexpected source call: %+v", source)
	}
	exists, err := service.HasLimitUpArchive(context.Background(), "2026-11-05")
	if err != nil || !exists {
		testingT.Fatalf("published event archive was not readable: exists=%v error=%v", exists, err)
	}
	quality, err := store.LimitUpArchiveQuality(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if err != nil || quality.Status != limitup.ArchiveStatusComplete || quality.LastRun == nil || quality.LastRun.Status != "success" {
		testingT.Fatalf("event run quality was not finalized: %+v error=%v", quality, err)
	}

	withoutSource := New(nil, store, logger).WithLimitUpSource(nil, limitup.ScopeDefault)
	exists, err = withoutSource.HasLimitUpArchive(context.Background(), "2026-11-05")
	if err != nil || !exists {
		testingT.Fatalf("completed archive should be detected without a live source: exists=%v error=%v", exists, err)
	}
}

func TestLimitUpEventArchiveFailureDoesNotPublish(testingT *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		testingT.Fatal(err)
	}
	defer store.Close()
	runAt := time.Date(2026, 11, 5, 16, 0, 0, 0, limitup.Shanghai())
	source := &fixedLimitUpSource{snapshot: limitup.Snapshot{
		TradeDate: "2026-11-04", Source: limitup.SourceTHS, Scope: limitup.ScopeDefault,
		FetchedAt: runAt, RawPages: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)},
	}}
	service := New(nil, store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithLimitUpSource(source, limitup.ScopeDefault)

	if err := service.CollectLimitUpEvents(context.Background(), runAt); err == nil {
		testingT.Fatal("mismatched event date was accepted")
	}
	exists, err := service.HasLimitUpArchive(context.Background(), "2026-11-05")
	if err != nil || exists {
		testingT.Fatalf("failed archive became visible: exists=%v error=%v", exists, err)
	}
	quality, err := store.LimitUpArchiveQuality(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if err != nil || quality.Status != limitup.ArchiveStatusFailed || quality.Complete {
		testingT.Fatalf("failed archive state was not recorded: %+v error=%v", quality, err)
	}
}
