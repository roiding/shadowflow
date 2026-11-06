package proxypool

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPreparationSizesByPendingRequestsAndConcurrency(testingT *testing.T) {
	for _, sample := range []struct {
		name        string
		requests    int
		concurrency int
		workers     int
		cap         int
		count       int
		minimum     int
	}{
		{"empty", 0, 4, 4, 8, 0, 0},
		{"single", 1, 4, 4, 8, 1, 1},
		{"two", 2, 4, 4, 8, 2, 2},
		{"four", 4, 4, 4, 8, 4, 4},
		{"five", 5, 4, 4, 8, 5, 4},
		{"full-market", 5500, 4, 4, 8, 6, 4},
		{"serial-relations", -1, 1, 4, 8, 2, 1},
		{"unknown-kline", -1, 4, 4, 8, 6, 4},
		{"lower-global-concurrency", 5500, 4, 2, 8, 3, 2},
		{"explicit-cap", 5500, 4, 4, 2, 2, 2},
		{"legacy-100-is-only-a-cap", 5500, 4, 4, 100, 6, 4},
	} {
		testingT.Run(sample.name, func(testingT *testing.T) {
			fixture := newControlFixture()
			manager := fixtureManager(testingT, fixture, sample.workers)
			manager.options.PrewarmCount = sample.cap
			if err := manager.PrepareForRequests(context.Background(), sample.requests, sample.concurrency, "https://history.example"); err != nil {
				testingT.Fatal(err)
			}
			status := manager.Status()
			if sample.count == 0 {
				if len(status.Prewarms) != 0 || fixture.prewarms != 0 {
					testingT.Fatal("empty work warmed unused proxies")
				}
				return
			}
			if len(status.Prewarms) != 1 || status.Prewarms[0].Requested != sample.count || status.Prewarms[0].Minimum != sample.minimum {
				testingT.Fatalf("wrong demand sizing: %+v", status.Prewarms)
			}
			if !reflect.DeepEqual(fixture.prewarmCounts, []int{sample.count}) {
				testingT.Fatalf("wrong prewarm request count: %v", fixture.prewarmCounts)
			}
		})
	}
}

func TestPartialPrewarmOnlyRequiresWorkingCapacity(testingT *testing.T) {
	for _, sample := range []struct {
		name       string
		state      string
		ready      int
		unfinished bool
		allowed    bool
	}{
		{"enough-without-spares", "PARTIAL", 4, false, true},
		{"too-few-workers", "PARTIAL", 3, false, false},
		{"missing-completion-time", "PARTIAL", 4, true, false},
		{"failed-is-not-partial", "FAILED", 4, false, false},
		{"cancelled-is-not-partial", "CANCELLED", 6, false, false},
	} {
		testingT.Run(sample.name, func(testingT *testing.T) {
			fixture := newControlFixture()
			fixture.state, fixture.partialReady, fixture.noFinishedAt = sample.state, sample.ready, sample.unfinished
			manager := fixtureManager(testingT, fixture, 4)
			err := manager.PrepareForRequests(context.Background(), 5000, 4, "https://history.example")
			if sample.allowed && err != nil || !sample.allowed && !errors.Is(err, ErrPrewarmIncomplete) {
				testingT.Fatalf("partial result admission mismatch: %v", err)
			}
		})
	}
}

func TestLargerWorkloadDoesNotReuseInsufficientWarmup(testingT *testing.T) {
	fixture := newControlFixture()
	fixture.ready = make(chan struct{})
	manager := fixtureManager(testingT, fixture, 4)
	target := "https://history.example"
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- manager.EnsurePrepared(context.Background(), target) }()
	waitForWarmupWaiters(testingT, manager, 1)
	go func() { second <- manager.PrepareForRequests(context.Background(), 5000, 4, target) }()
	waitForWarmupWaiters(testingT, manager, 2)
	close(fixture.ready)
	if err := <-first; err != nil {
		testingT.Fatal(err)
	}
	if err := <-second; err != nil {
		testingT.Fatal(err)
	}
	if !reflect.DeepEqual(fixture.prewarmCounts, []int{1, 6}) {
		testingT.Fatalf("larger batch escaped adequate preparation: %v", fixture.prewarmCounts)
	}
}

func waitForWarmupWaiters(testingT *testing.T, manager *Manager, count int) {
	testingT.Helper()
	deadline := time.After(time.Second)
	for {
		manager.mu.Lock()
		current := manager.warmups[validationScope{Mode: "tls", Target: "history.example:443"}]
		joined := current != nil && current.waiters >= count
		manager.mu.Unlock()
		if joined {
			return
		}
		select {
		case <-deadline:
			testingT.Fatal("waiters did not join shared prewarm")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestSerialRequestsReuseOneOfFourGlobalWorkers(testingT *testing.T) {
	fixture := newControlFixture()
	manager := fixtureManager(testingT, fixture, 4)
	target := "https://history.example"
	for range 12 {
		worker, err := manager.Borrow(context.Background(), target)
		if err != nil {
			testingT.Fatal(err)
		}
		_, err = worker.Get(context.Background(), target, nil)
		worker.Release()
		if err != nil {
			testingT.Fatal(err)
		}
	}
	if fixture.acquires != 1 || !reflect.DeepEqual(fixture.prewarmCounts, []int{1}) {
		testingT.Fatalf("serial traffic consumed idle worker proxies: acquire=%d prewarm=%v", fixture.acquires, fixture.prewarmCounts)
	}
	fixture.mu.Lock()
	fixture.now = fixture.now.Add(31 * time.Second)
	fixture.mu.Unlock()
	worker, err := manager.Borrow(context.Background(), target)
	if err != nil {
		testingT.Fatal(err)
	}
	_, err = worker.Get(context.Background(), target, nil)
	worker.Release()
	if err != nil || manager.Status().LeaseRenewals != 1 || len(fixture.bindings) != 1 {
		testingT.Fatalf("serial request abandoned its renewable binding: %v status=%+v", err, manager.Status())
	}
}

func TestConcurrentReplenishmentOnlyWarmsMissingCapacity(testingT *testing.T) {
	fixture := newControlFixture()
	manager := fixtureManager(testingT, fixture, 4)
	target := "https://history.example"
	if err := manager.PrepareForRequests(context.Background(), 5000, 4, target); err != nil {
		testingT.Fatal(err)
	}
	workers := make([]*Worker, 0, 4)
	for range 4 {
		worker, err := manager.Borrow(context.Background(), target)
		if err != nil {
			testingT.Fatal(err)
		}
		workers = append(workers, worker)
		defer worker.Release()
		if _, err := worker.Get(context.Background(), target, nil); err != nil {
			testingT.Fatal(err)
		}
	}
	fixture.mu.Lock()
	fixture.ready = make(chan struct{})
	fixture.state, fixture.partialReady = "PARTIAL", 1
	fixture.mu.Unlock()
	results := make(chan error, 4)
	for _, worker := range workers {
		go func() { results <- worker.Replenish(context.Background(), target) }()
	}
	waitForWarmupWaiters(testingT, manager, 4)
	close(fixture.ready)
	for range 4 {
		if err := <-results; err != nil {
			testingT.Fatalf("refill waited for a full fresh worker set: %v", err)
		}
	}
	if !reflect.DeepEqual(fixture.prewarmCounts, []int{6, 2}) {
		testingT.Fatalf("refill repeated whole-batch preparation: %v", fixture.prewarmCounts)
	}
}
