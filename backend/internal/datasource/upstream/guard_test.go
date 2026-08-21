package upstream

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type failingTransport struct{ calls int }

func (f *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls++
	return &http.Response{StatusCode: http.StatusBadGateway, Body: http.NoBody}, nil
}

func TestGuardOpensCircuitAfterFailures(t *testing.T) {
	transport := &failingTransport{}
	guard := New(&http.Client{Transport: transport}, Options{MaxConcurrency: 1, RatePerSecond: 1000, FailureThreshold: 3, OpenDuration: time.Minute})
	for range 3 {
		request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test", nil)
		if _, err := guard.Do(context.Background(), request); err == nil {
			t.Fatal("expected failure")
		}
	}
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test", nil)
	if _, err := guard.Do(context.Background(), request); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected open circuit, got %v", err)
	}
	if transport.calls != 3 {
		t.Fatalf("circuit did not stop requests: calls=%d", transport.calls)
	}
	if state := guard.State(); state != StateOpen {
		t.Fatalf("state=%s", state)
	}
}
