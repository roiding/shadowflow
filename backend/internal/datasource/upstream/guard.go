package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

var ErrCircuitOpen = errors.New("upstream circuit is open")

type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half-open"
)

type Options struct {
	MaxConcurrency    int
	RatePerSecond     float64
	FailureThreshold  int
	OpenDuration      time.Duration
	RecoverySuccesses int
}

type circuitState struct {
	failures          []time.Time
	openUntil         time.Time
	halfOpenProbe     bool
	halfOpenSuccesses int
}

type Guard struct {
	httpClient *http.Client
	options    Options
	semaphore  chan struct{}
	rateMu     sync.Mutex
	nextSend   time.Time
	mu         sync.Mutex
	circuits   map[string]*circuitState
}

type guardedBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *guardedBody) Read(buffer []byte) (int, error) {
	n, err := b.ReadCloser.Read(buffer)
	if err != nil {
		b.once.Do(b.release)
	}
	return n, err
}

func (b *guardedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

func New(httpClient *http.Client, options Options) *Guard {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if options.MaxConcurrency <= 0 {
		options.MaxConcurrency = 4
	}
	if options.RatePerSecond <= 0 {
		options.RatePerSecond = 8
	}
	if options.FailureThreshold <= 0 {
		options.FailureThreshold = 10
	}
	if options.OpenDuration <= 0 {
		options.OpenDuration = 30 * time.Second
	}
	if options.RecoverySuccesses <= 0 {
		options.RecoverySuccesses = 3
	}
	return &Guard{
		httpClient: httpClient, options: options,
		semaphore: make(chan struct{}, options.MaxConcurrency),
		circuits:  make(map[string]*circuitState),
	}
}

func (g *Guard) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	key := circuitKey(request)
	probe, err := g.acquireProbe(key)
	if err != nil {
		return nil, err
	}
	select {
	case g.semaphore <- struct{}{}:
	case <-ctx.Done():
		g.releaseProbe(key, probe)
		return nil, ctx.Err()
	}
	releaseSlot := func() { <-g.semaphore }
	if err := g.waitSend(ctx); err != nil {
		releaseSlot()
		g.releaseProbe(key, probe)
		return nil, err
	}
	response, err := g.httpClient.Do(request)
	if err != nil {
		releaseSlot()
		// A cancelled or deadline-exceeded caller context says nothing about
		// upstream health; recording it as a failure lets a batch abort (e.g.
		// consecutive-failure cutoff or process shutdown) trip the breaker and
		// lock out the host for the next scheduled task.
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			g.record(key, false)
		}
		g.releaseProbe(key, probe)
		return nil, err
	}
	healthy := response.StatusCode < http.StatusInternalServerError &&
		response.StatusCode != http.StatusRequestTimeout &&
		response.StatusCode != http.StatusTooEarly &&
		response.StatusCode != http.StatusTooManyRequests &&
		response.StatusCode != http.StatusUnauthorized &&
		response.StatusCode != http.StatusForbidden
	g.record(key, healthy)
	g.releaseProbe(key, probe)
	if !healthy {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		releaseSlot()
		return nil, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	response.Body = &guardedBody{ReadCloser: response.Body, release: releaseSlot}
	return response, nil
}

// State reports the most severe state among upstream hosts. Breaker decisions
// are isolated by host so failures on a delayed quote endpoint cannot disable
// collection from an otherwise healthy host.
func (g *Guard) State() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	state := StateClosed
	for _, circuit := range g.circuits {
		if !circuit.openUntil.IsZero() && now.Before(circuit.openUntil) {
			return StateOpen
		}
		if !circuit.openUntil.IsZero() {
			state = StateHalfOpen
		}
	}
	return state
}

func circuitKey(request *http.Request) string {
	if request == nil || request.URL == nil {
		return "unknown"
	}
	if request.URL.Host != "" {
		return request.URL.Host
	}
	return "unknown"
}

func (g *Guard) waitSend(ctx context.Context) error {
	g.rateMu.Lock()
	defer g.rateMu.Unlock()
	if wait := time.Until(g.nextSend); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	g.nextSend = time.Now().Add(time.Duration(float64(time.Second) / g.options.RatePerSecond))
	return nil
}

func (g *Guard) acquireProbe(key string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	circuit := g.circuits[key]
	if circuit == nil {
		circuit = &circuitState{}
		g.circuits[key] = circuit
	}
	now := time.Now()
	if circuit.openUntil.IsZero() {
		return false, nil
	}
	if now.Before(circuit.openUntil) {
		return false, ErrCircuitOpen
	}
	if circuit.halfOpenProbe {
		return false, ErrCircuitOpen
	}
	circuit.halfOpenProbe = true
	return true, nil
}

func (g *Guard) releaseProbe(key string, probe bool) {
	if !probe {
		return
	}
	g.mu.Lock()
	if circuit := g.circuits[key]; circuit != nil {
		circuit.halfOpenProbe = false
	}
	g.mu.Unlock()
}

func (g *Guard) record(key string, healthy bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	circuit := g.circuits[key]
	if circuit == nil {
		circuit = &circuitState{}
		g.circuits[key] = circuit
	}
	now := time.Now()
	if !circuit.openUntil.IsZero() && now.Before(circuit.openUntil) {
		return
	}
	if healthy {
		if !circuit.openUntil.IsZero() {
			circuit.halfOpenSuccesses++
			if circuit.halfOpenSuccesses >= g.options.RecoverySuccesses {
				resetCircuit(circuit)
			}
			return
		}
		circuit.failures = nil
		return
	}
	if !circuit.openUntil.IsZero() {
		circuit.openUntil = now.Add(g.options.OpenDuration)
		circuit.halfOpenSuccesses = 0
		return
	}
	circuit.failures = append(circuit.failures, now)
	cutoff := now.Add(-time.Minute)
	index := 0
	for index < len(circuit.failures) && circuit.failures[index].Before(cutoff) {
		index++
	}
	circuit.failures = append([]time.Time(nil), circuit.failures[index:]...)
	if len(circuit.failures) >= g.options.FailureThreshold {
		circuit.openUntil = now.Add(g.options.OpenDuration)
		circuit.halfOpenSuccesses = 0
		circuit.failures = nil
	}
}

func resetCircuit(circuit *circuitState) {
	circuit.openUntil = time.Time{}
	circuit.halfOpenSuccesses = 0
	circuit.halfOpenProbe = false
	circuit.failures = nil
}
