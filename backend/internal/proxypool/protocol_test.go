package proxypool

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrewarmHandlesActualPendingStateAndRaisedTarget(testingT *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/api/v1/prewarm" {
			writer.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(writer, `{"job_id":"warm_actual","kind":"prewarm","state":"PENDING","target":100,"ready":0}`)
			return
		}
		state := "RUNNING"
		var finishedAt *time.Time
		if polls.Add(1) >= 2 {
			state = "COMPLETED"
			now := time.Now().UTC()
			finishedAt = &now
		}
		_ = json.NewEncoder(writer).Encode(prewarmJob{ID: "warm_actual", Kind: "prewarm", State: state,
			Target: 120, Ready: 120, FinishedAt: finishedAt})
	}))
	defer server.Close()
	manager, err := NewManager(ManagerOptions{URL: server.URL, Token: "test-key", Workers: 1,
		PollInterval: time.Millisecond, PrewarmTimeout: time.Second, ControlTimeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		testingT.Fatal(err)
	}
	defer manager.Stop()
	if err := manager.Prepare(context.Background(), "https://push2his.eastmoney.com"); err != nil {
		testingT.Fatalf("actual prewarm wire contract was rejected: %v", err)
	}
	status := manager.Status().Prewarms[0]
	if polls.Load() != 2 || status.Requested != 2 || status.Minimum != 1 || status.Ready != 120 || status.State != "COMPLETED" {
		testingT.Fatalf("wrong original threshold or early release: %+v polls=%d", status, polls.Load())
	}
}

func TestLeaseRenewalReservesTheBusinessRequestBudget(testingT *testing.T) {
	fixture := newControlFixture()
	manager := fixtureManager(testingT, fixture, 1)
	worker, err := manager.Borrow(context.Background())
	if err != nil {
		testingT.Fatal(err)
	}
	defer worker.Release()
	target := "https://push2his.eastmoney.com/api/qt/stock/kline/get"
	first, err := worker.Get(context.Background(), target, nil, 12*time.Second)
	if err != nil {
		testingT.Fatal(err)
	}
	fixture.mu.Lock()
	fixture.now = fixture.now.Add(20 * time.Second)
	fixture.mu.Unlock()
	renewed, err := worker.Get(context.Background(), target, nil, 12*time.Second)
	if err != nil || first.ID == renewed.ID || first.Key != renewed.Key {
		testingT.Fatalf("ten seconds of remaining lease was reused for a twelve-second request: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if strings.Contains(strings.Join(fixture.events, "\n"), "/feedback") || fixture.acquires != 2 {
		testingT.Fatal("healthy renewal was classified as proxy failure")
	}
}

func TestNearExpiryBindingIsRetiredWithoutFailureFeedback(testingT *testing.T) {
	fixture := newControlFixture()
	manager := fixtureManager(testingT, fixture, 1)
	worker, err := manager.Borrow(context.Background())
	if err != nil {
		testingT.Fatal(err)
	}
	defer worker.Release()
	target := "https://push2his.eastmoney.com"
	first, err := worker.Get(context.Background(), target, nil, 12*time.Second)
	if err != nil {
		testingT.Fatal(err)
	}
	current := worker.bindings[first.scope]
	current.expiresAt = fixture.now.Add(5 * time.Second)
	current.lease.ExpiresAt = current.expiresAt
	replacement, err := worker.Get(context.Background(), target, nil, 12*time.Second)
	if err != nil || replacement.Key == first.Key {
		testingT.Fatalf("expiring binding was renewed beyond its lifetime: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if strings.Contains(strings.Join(fixture.events, "\n"), "/feedback") {
		testingT.Fatal("expiring binding was falsely marked unhealthy")
	}
}

func TestFailedBindingReleaseIsCounted(testingT *testing.T) {
	fixture := newControlFixture()
	manager := fixtureManager(testingT, fixture, 1)
	worker, err := manager.Borrow(context.Background())
	if err != nil {
		testingT.Fatal(err)
	}
	defer worker.Release()
	lease, err := worker.Get(context.Background(), "https://push2his.eastmoney.com", nil)
	if err != nil {
		testingT.Fatal(err)
	}
	fixture.mu.Lock()
	fixture.failDelete = true
	fixture.mu.Unlock()
	err = worker.Fail(context.Background(), lease, "transport_timeout")
	fixture.mu.Lock()
	fixture.failDelete = false
	fixture.mu.Unlock()
	if err == nil || manager.Status().CleanupErrors != 1 {
		testingT.Fatalf("failed binding release was not counted once: %v status=%+v", err, manager.Status())
	}
}
