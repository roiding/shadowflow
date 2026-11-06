package proxypool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type controlFixture struct {
	mu            sync.Mutex
	now           time.Time
	events        []string
	prewarms      int
	polls         int
	acquires      int
	ready         chan struct{}
	state         string
	shortfall     bool
	wrongScope    bool
	staleProof    bool
	failDelete    bool
	duplicate     bool
	surplus       bool
	partialReady  int
	noFinishedAt  bool
	prewarmCounts []int
	jobCounts     map[string]int
	acquireBodies []map[string]any
	bindings      map[string]int
}

func newControlFixture() *controlFixture {
	return &controlFixture{now: time.Now().UTC(), state: "COMPLETED", bindings: make(map[string]int), jobCounts: make(map[string]int)}
}

func (fixture *controlFixture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.events = append(fixture.events, request.Method+" "+request.URL.Path)
	writer.Header().Set("Content-Type", "application/json")
	if request.Header.Get("Authorization") != "Bearer test-project-key" {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case request.URL.Path == "/api/v1/prewarm":
		var body struct {
			Count      int             `json:"count"`
			Validation validationScope `json:"validation"`
		}
		if request.Method != http.MethodPost || json.NewDecoder(request.Body).Decode(&body) != nil || body.Count < 1 || body.Count > 100 {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		fixture.prewarms++
		fixture.prewarmCounts = append(fixture.prewarmCounts, body.Count)
		jobID := fmt.Sprintf("job-%d", fixture.prewarms)
		fixture.jobCounts[jobID] = body.Count
		writer.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(writer).Encode(prewarmJob{ID: jobID, Kind: "prewarm", State: "QUEUED", Target: body.Count})
	case strings.HasPrefix(request.URL.Path, "/api/v1/jobs/"):
		if strings.HasSuffix(request.URL.Path, "/cancel") {
			_, _ = io.WriteString(writer, `{}`)
			return
		}
		fixture.polls++
		state := fixture.state
		if fixture.ready != nil {
			select {
			case <-fixture.ready:
			default:
				state = "RUNNING"
			}
		}
		jobID := strings.TrimPrefix(request.URL.Path, "/api/v1/jobs/")
		count := fixture.jobCounts[jobID]
		ready := count
		if fixture.surplus {
			ready = 120
		}
		if state != "COMPLETED" {
			ready = fixture.partialReady
		}
		var finishedAt *time.Time
		if !fixture.noFinishedAt {
			finished := fixture.now
			finishedAt = &finished
		}
		_ = json.NewEncoder(writer).Encode(prewarmJob{ID: jobID, Kind: "prewarm",
			State: state, Target: count, Ready: ready, FinishedAt: finishedAt})
	case request.URL.Path == "/api/v1/proxies/acquire":
		var body map[string]any
		if json.NewDecoder(request.Body).Decode(&body) != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		fixture.acquires++
		fixture.acquireBodies = append(fixture.acquireBodies, body)
		if fixture.shortfall {
			_, _ = io.WriteString(writer, `{"requested":1,"delivered":0,"missing":1,"items":[],"reason":"NO_VALIDATED_RESOURCES"}`)
			return
		}
		sessionID, _ := body["session_id"].(string)
		identity, found := fixture.bindings[sessionID]
		if !found {
			identity = fixture.acquires
			fixture.bindings[sessionID] = identity
		}
		if fixture.duplicate {
			identity = 1
		}
		changed := !found
		password := ""
		item := acquiredItem{LeaseID: fmt.Sprintf("lease-%d", fixture.acquires), SourceID: "public-list", LeaseExpiresAt: fixture.now.Add(30 * time.Second)}
		item.Connection.Transport, item.Connection.Host, item.Connection.Port = "http", fmt.Sprintf("proxy-%d.test", identity), 8080
		item.Connection.Username, item.Connection.Password = "fixture-user", &password
		validation, _ := body["validation"].(map[string]any)
		item.Validation.Mode, _ = validation["mode"].(string)
		item.Validation.Target, _ = validation["target"].(string)
		item.Validation.CompletedAt, item.Validation.ValidUntil = fixture.now, fixture.now.Add(30*time.Second)
		if fixture.wrongScope {
			item.Validation.Target = "other.example:443"
		}
		if fixture.staleProof {
			item.Validation.ValidUntil = fixture.now.Add(-time.Second)
		}
		_ = json.NewEncoder(writer).Encode(acquireResponse{Requested: 1, Delivered: 1, Items: []acquiredItem{item}, SessionID: sessionID,
			SessionChanged: &changed, BindingVersion: 1, BindingExpiresAt: fixture.now.Add(10 * time.Minute)})
	case strings.HasPrefix(request.URL.Path, "/api/v1/sessions/"):
		if fixture.failDelete {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		delete(fixture.bindings, strings.TrimPrefix(request.URL.Path, "/api/v1/sessions/"))
		_, _ = io.WriteString(writer, `{}`)
	case strings.HasPrefix(request.URL.Path, "/api/v1/leases/"):
		_, _ = io.WriteString(writer, `{}`)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func fixtureManager(test *testing.T, fixture *controlFixture, workers int) *Manager {
	test.Helper()
	server := httptest.NewServer(fixture)
	test.Cleanup(server.Close)
	manager, err := NewManager(ManagerOptions{
		URL: server.URL, Token: "test-project-key", Workers: workers, MaxAttempts: 3,
		ControlTimeout: time.Second, AcquireTimeout: time.Second, PrewarmTimeout: time.Second,
		PollInterval: 5 * time.Millisecond, Protocols: []string{"http"}, SourceIDs: []string{"public-list"},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			return fixture.now
		},
	})
	if err != nil {
		test.Fatal(err)
	}
	test.Cleanup(manager.Stop)
	return manager
}

func TestManagedPrewarmWaitsForCompletionAndCoalesces(testingT *testing.T) {
	fixture := newControlFixture()
	fixture.ready = make(chan struct{})
	manager := fixtureManager(testingT, fixture, 4)
	results := make(chan error, 4)
	for range 4 {
		go func() {
			results <- manager.Prepare(context.Background(), "https://push2his.eastmoney.com/api/qt/stock/kline/get")
		}()
	}
	deadline := time.After(time.Second)
	for {
		fixture.mu.Lock()
		polled := fixture.polls > 0
		fixture.mu.Unlock()
		if polled {
			break
		}
		select {
		case <-deadline:
			testingT.Fatal("prewarm was not polled")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case err := <-results:
		testingT.Fatalf("prewarm returned before completion: %v", err)
	default:
	}
	if manager.Status().BusyWorkers != 0 {
		testingT.Fatal("prewarm consumed a business worker")
	}
	close(fixture.ready)
	for range 4 {
		if err := <-results; err != nil {
			testingT.Fatal(err)
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.prewarms != 1 || fixture.acquires != 0 {
		testingT.Fatalf("prewarm=%d acquire=%d", fixture.prewarms, fixture.acquires)
	}
}

func TestManagedPrewarmRejectsPartialAndCancelsOnDeadline(testingT *testing.T) {
	for _, state := range []string{"PARTIAL", "FAILED", "CANCELLED", "INTERRUPTED", "RUNNING"} {
		testingT.Run(state, func(test *testing.T) {
			fixture := newControlFixture()
			fixture.state = state
			manager := fixtureManager(test, fixture, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			err := manager.Prepare(ctx, "https://push2his.eastmoney.com")
			if state == "RUNNING" {
				if !errors.Is(err, context.DeadlineExceeded) {
					test.Fatalf("expected deadline, got %v", err)
				}
				manager.warmWG.Wait()
				fixture.mu.Lock()
				defer fixture.mu.Unlock()
				if !strings.Contains(strings.Join(fixture.events, "\n"), "/cancel") {
					test.Fatal("unfinished job was not cancelled")
				}
			} else if !errors.Is(err, ErrPrewarmIncomplete) {
				test.Fatalf("partial/failed prewarm was accepted: %v", err)
			}
		})
	}
}

func TestManagedPrewarmSeparatesTargetsAndRefreshesOldReceipt(testingT *testing.T) {
	fixture := newControlFixture()
	manager := fixtureManager(testingT, fixture, 1)
	for _, target := range []string{"https://push2.eastmoney.com", "https://push2his.eastmoney.com", "https://push2.eastmoney.com"} {
		if err := manager.Prepare(context.Background(), target); err != nil {
			testingT.Fatal(err)
		}
	}
	fixture.mu.Lock()
	if fixture.prewarms != 2 {
		testingT.Fatalf("different TLS scopes shared a receipt: %d", fixture.prewarms)
	}
	fixture.now = fixture.now.Add(31 * time.Second)
	fixture.mu.Unlock()
	if err := manager.Prepare(context.Background(), "https://push2.eastmoney.com"); err != nil {
		testingT.Fatal(err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.prewarms != 3 {
		testingT.Fatalf("old receipt bypassed preparation: %d", fixture.prewarms)
	}
}

func TestManagedWorkerReusesRenewsAndReplacesFailedBinding(testingT *testing.T) {
	fixture := newControlFixture()
	manager := fixtureManager(testingT, fixture, 1)
	worker, err := manager.Borrow(context.Background())
	if err != nil {
		testingT.Fatal(err)
	}
	defer worker.Release()
	target := "https://push2his.eastmoney.com/api/qt/stock/kline/get?klt=5"
	first, err := worker.Get(context.Background(), target, nil)
	if err != nil {
		testingT.Fatal(err)
	}
	second, err := worker.Get(context.Background(), target, nil)
	if err != nil || second.ID != first.ID {
		testingT.Fatalf("healthy lease was replaced: %v", err)
	}
	fixture.mu.Lock()
	fixture.now = fixture.now.Add(26 * time.Second)
	fixture.mu.Unlock()
	renewed, err := worker.Get(context.Background(), target, nil)
	if err != nil || renewed.ID == first.ID || renewed.Key != first.Key {
		testingT.Fatalf("healthy binding was not renewed: %v", err)
	}
	if err := worker.Fail(context.Background(), renewed, "transport_timeout"); err != nil {
		testingT.Fatal(err)
	}
	replacement, err := worker.Get(context.Background(), target, map[string]struct{}{renewed.Key: {}})
	if err != nil || replacement.Key == renewed.Key {
		testingT.Fatalf("failed proxy was not replaced: %v", err)
	}
	status := manager.Status()
	if status.LeaseAcquires != 3 || status.LeaseRenewals != 1 || status.ProxyFailures != 1 || status.ProxyCount != 1 {
		testingT.Fatalf("unexpected lifecycle counters: %+v", status)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, body := range fixture.acquireBodies {
		if body["count"] != float64(1) || body["mode"] != "sticky" || body["cd"] != float64(300) ||
			body["session_id"] != fixture.acquireBodies[0]["session_id"] {
			testingT.Fatalf("invalid acquire contract: %#v", body)
		}
	}
	events := strings.Join(fixture.events, "\n")
	feedback := strings.Index(events, "/leases/lease-2/feedback")
	release := strings.Index(events, "/leases/lease-2/release")
	deleteBinding := strings.Index(events, "DELETE /api/v1/sessions/")
	if feedback < 0 || release <= feedback || deleteBinding <= release {
		testingT.Fatalf("invalid failover order: %s", events)
	}
}

func TestManagedWorkerRejectsBadProofAndDoesNotBypassFailedDelete(testingT *testing.T) {
	for _, condition := range []string{"wrong-scope", "stale-proof", "failed-delete", "duplicate"} {
		testingT.Run(condition, func(test *testing.T) {
			fixture := newControlFixture()
			manager := fixtureManager(test, fixture, 1)
			fixture.wrongScope, fixture.staleProof = condition == "wrong-scope", condition == "stale-proof"
			worker, err := manager.Borrow(context.Background())
			if err != nil {
				test.Fatal(err)
			}
			defer worker.Release()
			target := "https://push2his.eastmoney.com"
			lease, err := worker.Get(context.Background(), target, nil)
			if condition == "wrong-scope" || condition == "stale-proof" {
				if err == nil {
					test.Fatal("invalid validation evidence was accepted")
				}
				return
			}
			if err != nil {
				test.Fatal(err)
			}
			fixture.mu.Lock()
			fixture.failDelete, fixture.duplicate = condition == "failed-delete", condition == "duplicate"
			fixture.mu.Unlock()
			_ = worker.Fail(context.Background(), lease, "transport_timeout")
			if _, err := worker.Get(context.Background(), target, map[string]struct{}{lease.Key: {}}); err == nil {
				test.Fatal("failed connection was reused")
			}
			fixture.mu.Lock()
			fixture.failDelete = false
			fixture.mu.Unlock()
		})
	}
}

func TestManagedWorkersEnforceGlobalLimitAndCancellation(testingT *testing.T) {
	manager := fixtureManager(testingT, newControlFixture(), 4)
	held := make([]*Worker, 0, 4)
	for range 4 {
		worker, err := manager.Borrow(context.Background())
		if err != nil {
			testingT.Fatal(err)
		}
		held = append(held, worker)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := manager.Borrow(ctx); !errors.Is(err, context.DeadlineExceeded) {
		testingT.Fatalf("fifth worker was not blocked: %v", err)
	}
	if manager.Status().BusyWorkers != 4 {
		testingT.Fatal("incorrect global worker count")
	}
	for _, worker := range held {
		worker.Release()
	}
}

func TestManagedConfigurationRejectsLegacyURLsAndUnsafeTransport(testingT *testing.T) {
	for _, rawURL := range []string{"https://pool.test/proxies", "https://user:secret@pool.test", "https://pool.test?token=secret"} {
		if _, err := NewManager(ManagerOptions{URL: rawURL, Token: "test"}); err == nil || strings.Contains(err.Error(), "secret") {
			testingT.Fatalf("invalid control URL accepted or leaked: %v", err)
		}
	}
	for _, protocol := range []string{"https", "socks5h"} {
		if _, err := NewManager(ManagerOptions{URL: "https://pool.test", Token: "test", Protocols: []string{protocol}}); err == nil {
			testingT.Fatalf("unsupported protocol %s accepted", protocol)
		}
	}
}

func TestManagedPrewarmAllowsSurplusReady(testingT *testing.T) {
	fixture := newControlFixture()
	fixture.surplus = true
	manager := fixtureManager(testingT, fixture, 1)
	if err := manager.Prepare(context.Background(), "https://push2his.eastmoney.com"); err != nil {
		testingT.Fatalf("surplus current Ready was rejected: %v", err)
	}
}

func TestOneCanceledPrewarmWaiterDoesNotCancelOtherWaiters(testingT *testing.T) {
	fixture := newControlFixture()
	fixture.ready = make(chan struct{})
	manager := fixtureManager(testingT, fixture, 1)
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- manager.Prepare(firstCtx, "https://push2his.eastmoney.com") }()
	go func() { second <- manager.Prepare(context.Background(), "https://push2his.eastmoney.com") }()
	deadline := time.After(time.Second)
	for {
		manager.mu.Lock()
		current := manager.warmups[validationScope{Mode: "tls", Target: "push2his.eastmoney.com:443"}]
		joined := current != nil && current.waiters == 2
		manager.mu.Unlock()
		if joined {
			break
		}
		select {
		case <-deadline:
			testingT.Fatal("waiters did not join one prewarm")
		case <-time.After(time.Millisecond):
		}
	}
	firstCancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		testingT.Fatalf("first waiter did not cancel: %v", err)
	}
	close(fixture.ready)
	if err := <-second; err != nil {
		testingT.Fatalf("one caller cancelled another caller's prewarm: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.prewarms != 1 || strings.Contains(strings.Join(fixture.events, "\n"), "/cancel") {
		testingT.Fatal("shared prewarm was replaced or cancelled")
	}
}
