package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScanGateRemainsHeldAfterHTTPTimeout(t *testing.T) {
	gate := scanConcurrencyGate()
	entered, release, workerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	worker := gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	tracked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(workerDone); worker.ServeHTTP(w, r) })
	first := httptest.NewRecorder()
	responseDone := make(chan struct{})
	go func() {
		defer close(responseDone)
		requestTimeout(20*time.Millisecond)(tracked).ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("scan did not start")
	}
	select {
	case <-responseDone:
	case <-time.After(time.Second):
		t.Fatal("request timeout did not return")
	}
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP timeout: %d", first.Code)
	}
	other := gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	second := httptest.NewRecorder()
	other.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/", nil))
	if second.Code != http.StatusServiceUnavailable || second.Header().Get("Retry-After") == "" || !strings.Contains(second.Body.String(), "scan_busy") {
		t.Fatalf("expired HTTP response released active scan: %d %s", second.Code, second.Body.String())
	}
	close(release)
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("scan did not stop")
	}
	third := httptest.NewRecorder()
	other.ServeHTTP(third, httptest.NewRequest(http.MethodGet, "/", nil))
	if third.Code != http.StatusOK {
		t.Fatalf("finished scan leaked slot: %d", third.Code)
	}
}
