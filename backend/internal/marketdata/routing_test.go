package marketdata

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeProvider struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeProvider) fetch(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func (f *fakeProvider) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestRouterCoolsFailedProviderForFollowingCalls(t *testing.T) {
	primary := &fakeProvider{err: errors.New("primary blocked")}
	backup := &fakeProvider{}
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	router, err := NewRouter("quotes", []NamedProvider[*fakeProvider]{
		{Name: "eastmoney", Source: primary},
		{Name: "ths", Source: backup},
	}, RouterOptions{
		Cooldown: time.Minute,
		Clock:    func() time.Time { return now },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := router.Do(context.Background(), func(_ context.Context, source *fakeProvider) error {
			return source.fetch(context.Background())
		}); err != nil {
			t.Fatal(err)
		}
	}
	if primary.count() != 1 || backup.count() != 3 {
		t.Fatalf("cooldown did not reuse backup: primary=%d backup=%d", primary.count(), backup.count())
	}
	now = now.Add(time.Minute)
	primary.err = nil
	if err := router.Do(context.Background(), func(_ context.Context, source *fakeProvider) error {
		return source.fetch(context.Background())
	}); err != nil {
		t.Fatal(err)
	}
	if primary.count() != 2 {
		t.Fatalf("expired cooldown did not restore primary: %d", primary.count())
	}
}

func TestRouterDoesNotRepeatCoolingProviders(t *testing.T) {
	primary := &fakeProvider{err: errors.New("primary blocked")}
	backup := &fakeProvider{err: errors.New("backup blocked")}
	router, err := NewRouter("kline", []NamedProvider[*fakeProvider]{
		{Name: "eastmoney", Source: primary},
		{Name: "ths", Source: backup},
	}, RouterOptions{Cooldown: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	operation := func(_ context.Context, source *fakeProvider) error {
		return source.fetch(context.Background())
	}
	if err := router.Do(context.Background(), operation); err == nil {
		t.Fatal("expected both-provider failure")
	}
	if err := router.Do(context.Background(), operation); !errors.Is(err, ErrProvidersUnavailable) {
		t.Fatalf("expected cooldown failure, got %v", err)
	}
	if primary.count() != 1 || backup.count() != 1 {
		t.Fatalf("cooling providers were retried: primary=%d backup=%d", primary.count(), backup.count())
	}
}

func TestRouterContinuesAfterBackupFailure(t *testing.T) {
	primary := &fakeProvider{err: errors.New("primary blocked")}
	backup := &fakeProvider{err: errors.New("backup blocked")}
	third := &fakeProvider{}
	router, err := NewRouter("quotes", []NamedProvider[*fakeProvider]{
		{Name: "eastmoney", Source: primary},
		{Name: "ths", Source: backup},
		{Name: "future", Source: third},
	}, RouterOptions{Cooldown: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := router.Do(context.Background(), func(_ context.Context, source *fakeProvider) error {
		return source.fetch(context.Background())
	}); err != nil {
		t.Fatal(err)
	}
	if third.count() != 1 {
		t.Fatalf("third provider was not attempted: %d", third.count())
	}
}
