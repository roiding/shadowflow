package upstream

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type sequenceTransport struct {
	responses []int
	calls     int
}

func (t *sequenceTransport) RoundTrip(*http.Request) (*http.Response, error) {
	status := t.responses[min(t.calls, len(t.responses)-1)]
	t.calls++
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func TestGuardReopensOnFailedProbeAndRecoversAfterSuccesses(t *testing.T) {
	transport := &sequenceTransport{responses: []int{503, 503, 503, 503, 200, 200, 200, 200}}
	guard := New(&http.Client{Transport: transport}, Options{MaxConcurrency: 1, RatePerSecond: 1000, FailureThreshold: 3, OpenDuration: time.Minute, RecoverySuccesses: 3})
	send := func() error {
		request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test", nil)
		response, err := guard.Do(context.Background(), request)
		if response != nil {
			_ = response.Body.Close()
		}
		return err
	}
	for range 3 {
		if err := send(); err == nil {
			t.Fatal("expected initial upstream failure")
		}
	}
	if err := send(); err != ErrCircuitOpen {
		t.Fatalf("expected open circuit, got %v", err)
	}
	guard.mu.Lock()
	guard.circuits["example.test"].openUntil = time.Now().Add(-time.Second)
	guard.mu.Unlock()
	if err := send(); err == nil {
		t.Fatal("expected failed half-open probe")
	}
	if state := guard.State(); state != StateOpen {
		t.Fatalf("failed probe should reopen circuit, got %s", state)
	}
	guard.mu.Lock()
	guard.circuits["example.test"].openUntil = time.Now().Add(-time.Second)
	guard.mu.Unlock()
	for range 3 {
		if err := send(); err != nil {
			t.Fatalf("expected recovery success: %v", err)
		}
	}
	if state := guard.State(); state != StateClosed {
		t.Fatalf("expected closed circuit after recovery, got %s", state)
	}
	if err := send(); err != nil {
		t.Fatalf("expected closed circuit request to pass: %v", err)
	}
	if transport.calls != 8 {
		t.Fatalf("unexpected call count: %d", transport.calls)
	}
}
