package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
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

func TestGuardIsolatesCircuitsByHost(t *testing.T) {
	transport := &failingTransport{}
	guard := New(&http.Client{Transport: transport}, Options{MaxConcurrency: 1, RatePerSecond: 1000, FailureThreshold: 1, OpenDuration: time.Minute})
	failed, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://bad.example/path", nil)
	if _, err := guard.Do(context.Background(), failed); err == nil {
		t.Fatal("expected first host to fail")
	}
	other, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://other.example/path", nil)
	if _, err := guard.Do(context.Background(), other); errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("failure on bad.example opened other.example: %v", err)
	}
	if transport.calls != 2 {
		t.Fatalf("healthy host did not reach transport: calls=%d", transport.calls)
	}
}

func TestGuardTreatsForbiddenAsFailure(t *testing.T) {
	transport := &statusTransport{status: http.StatusForbidden}
	guard := New(&http.Client{Transport: transport}, Options{MaxConcurrency: 1, RatePerSecond: 1000, FailureThreshold: 1, OpenDuration: time.Minute})
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test", nil)
	if _, err := guard.Do(context.Background(), request); err == nil {
		t.Fatal("expected forbidden response to fail")
	}
	if state := guard.State(); state != StateOpen {
		t.Fatalf("state=%s, want open", state)
	}
}

type statusTransport struct{ status int }

func (t *statusTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: t.status, Body: http.NoBody}, nil
}

type countingTransport struct {
	calls atomic.Int32
}

func (t *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

func TestGuardHoldsConcurrencySlotUntilResponseBodyCloses(t *testing.T) {
	transport := &countingTransport{}
	guard := New(&http.Client{Transport: transport}, Options{MaxConcurrency: 1, RatePerSecond: 100000})
	firstRequest, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test/first", nil)
	first, err := guard.Do(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}

	secondDone := make(chan error, 1)
	go func() {
		request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test/second", nil)
		response, err := guard.Do(context.Background(), request)
		if response != nil {
			_ = response.Body.Close()
		}
		secondDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if calls := transport.calls.Load(); calls != 1 {
		t.Fatalf("second request started before first body closed: calls=%d", calls)
	}
	if err := first.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second request did not acquire released concurrency slot")
	}
}

func TestCanceledRateWaitDoesNotReserveFutureSlot(t *testing.T) {
	transport := &countingTransport{}
	guard := New(&http.Client{Transport: transport}, Options{MaxConcurrency: 1, RatePerSecond: 10})
	firstRequest, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test/first", nil)
	first, err := guard.Do(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Body.Close()

	canceledCtx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	canceledRequest, _ := http.NewRequestWithContext(canceledCtx, http.MethodGet, "https://example.test/canceled", nil)
	if _, err := guard.Do(canceledCtx, canceledRequest); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled rate wait error=%v", err)
	}

	time.Sleep(105 * time.Millisecond)
	thirdCtx, thirdCancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer thirdCancel()
	thirdRequest, _ := http.NewRequestWithContext(thirdCtx, http.MethodGet, "https://example.test/third", nil)
	third, err := guard.Do(thirdCtx, thirdRequest)
	if err != nil {
		t.Fatalf("canceled waiter delayed the next request: %v", err)
	}
	_ = third.Body.Close()
}
