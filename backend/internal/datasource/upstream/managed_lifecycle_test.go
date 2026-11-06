package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/proxypool"
)

func TestManagerShutdownCancelsBusinessAndReleasesLeases(testingT *testing.T) {
	manager, fixture := newManagedFixture(testingT, 1)
	started := make(chan struct{})
	transport := NewManagedTransport(bodyTestTransport(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	}), manager, time.Minute)
	finished := make(chan error, 1)
	go func() {
		_, err := (&http.Client{Transport: transport}).Get("https://push2his.eastmoney.com/api/qt/stock/kline/get")
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		testingT.Fatal("business request did not start")
	}
	manager.Stop()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		testingT.Fatalf("shutdown did not cancel business: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.feedback != 0 || fixture.releases != 1 || fixture.deletes != 1 {
		testingT.Fatalf("shutdown cleanup: feedback=%d releases=%d deletes=%d", fixture.feedback, fixture.releases, fixture.deletes)
	}
}

func TestLeaseDeadlineDoesNotPoisonProxyHealth(testingT *testing.T) {
	fixture := &managedFixture{bindings: make(map[string]int)}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder := httptest.NewRecorder()
		fixture.handle(recorder, request)
		body := recorder.Body.Bytes()
		if request.URL.Path == "/api/v1/proxies/acquire" {
			var response map[string]any
			if json.Unmarshal(body, &response) == nil {
				items, _ := response["items"].([]any)
				if len(items) == 1 {
					item := items[0].(map[string]any)
					if item["lease_id"] == "lease-1" {
						item["lease_expires_at"] = time.Now().Add(1400 * time.Millisecond).UTC()
						body, _ = json.Marshal(response)
					}
				}
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(recorder.Code)
		_, _ = writer.Write(body)
	}))
	defer server.Close()
	manager, err := proxypool.NewManager(proxypool.ManagerOptions{
		URL: server.URL, Token: "project-only-token", Workers: 1, MaxAttempts: 3,
		ControlTimeout: time.Second, AcquireTimeout: time.Second, PrewarmTimeout: time.Second, PollInterval: time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		testingT.Fatal(err)
	}
	defer manager.Stop()
	transport := NewManagedTransport(bodyTestTransport(func(request *http.Request) (*http.Response, error) {
		if strings.HasPrefix(request.URL.Host, "proxy-1.") {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}
		return managedResponse(http.StatusOK, `{"rc":0,"data":{}}`), nil
	}), manager, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://push2his.eastmoney.com/api/qt/stock/kline/get", nil)
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		testingT.Fatal(err)
	}
	_ = response.Body.Close()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.feedback != 0 || fixture.acquires != 2 || fixture.deletes != 1 || manager.Status().ProxyFailures != 0 {
		testingT.Fatalf("lease expiry poisoned proxy health: feedback=%d acquires=%d deletes=%d", fixture.feedback, fixture.acquires, fixture.deletes)
	}
}

func TestProxySupplyFailureDoesNotTripTargetCircuit(testingT *testing.T) {
	manager, fixture := newManagedFixture(testingT, 1)
	fixture.empty = true
	transport := NewManagedTransport(bodyTestTransport(func(*http.Request) (*http.Response, error) {
		testingT.Error("empty pool reached business transport")
		return managedResponse(http.StatusOK, `{}`), nil
	}), manager, time.Second)
	guard := New(&http.Client{Transport: transport}, Options{FailureThreshold: 1, RatePerSecond: 100})
	request, _ := http.NewRequest(http.MethodGet, "https://push2his.eastmoney.com/api/qt/stock/kline/get", nil)
	if _, err := guard.Do(context.Background(), request); !errors.Is(err, proxypool.ErrNoProxy) {
		testingT.Fatalf("missing proxy supply failure: %v", err)
	}
	if guard.State() != StateClosed {
		testingT.Fatal("proxy supply outage opened the Eastmoney target circuit")
	}
}
