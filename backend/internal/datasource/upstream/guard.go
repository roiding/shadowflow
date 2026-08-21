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

type Guard struct {
	httpClient        *http.Client
	options           Options
	semaphore         chan struct{}
	mu                sync.Mutex
	nextSend          time.Time
	failures          []time.Time
	openUntil         time.Time
	halfOpenProbe     bool
	halfOpenSuccesses int
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
	}
}

func (g *Guard) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := g.waitSend(ctx); err != nil {
		return nil, err
	}
	probe, err := g.acquireProbe()
	if err != nil {
		return nil, err
	}
	select {
	case g.semaphore <- struct{}{}:
	case <-ctx.Done():
		g.releaseProbe(probe)
		return nil, ctx.Err()
	}
	response, err := g.httpClient.Do(request)
	if err != nil {
		<-g.semaphore
		g.record(false)
		g.releaseProbe(probe)
		return nil, err
	}
	healthy := response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests
	<-g.semaphore
	g.record(healthy)
	g.releaseProbe(probe)
	if !healthy {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return nil, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	return response, nil
}

func (g *Guard) State() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if g.openUntil.IsZero() {
		return StateClosed
	}
	if now.Before(g.openUntil) {
		return StateOpen
	}
	return StateHalfOpen
}

func (g *Guard) waitSend(ctx context.Context) error {
	g.mu.Lock()
	now := time.Now()
	wait := time.Duration(0)
	if g.nextSend.After(now) {
		wait = g.nextSend.Sub(now)
	}
	g.nextSend = maxTime(now, g.nextSend).Add(time.Duration(float64(time.Second) / g.options.RatePerSecond))
	g.mu.Unlock()
	if wait == 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (g *Guard) acquireProbe() (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if g.openUntil.IsZero() || now.After(g.openUntil) {
		if g.openUntil.IsZero() {
			return false, nil
		}
		if g.halfOpenProbe {
			return false, ErrCircuitOpen
		}
		g.halfOpenProbe = true
		return true, nil
	}
	return false, ErrCircuitOpen
}

func (g *Guard) releaseProbe(probe bool) {
	if probe {
		g.mu.Lock()
		g.halfOpenProbe = false
		g.mu.Unlock()
	}
}

func (g *Guard) record(healthy bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if !g.openUntil.IsZero() && now.Before(g.openUntil) {
		return
	}
	if healthy {
		if !g.openUntil.IsZero() {
			g.halfOpenSuccesses++
			if g.halfOpenSuccesses >= g.options.RecoverySuccesses {
				g.resetLocked()
			}
			return
		}
		g.failures = nil
		return
	}
	if !g.openUntil.IsZero() {
		g.openUntil = now.Add(g.options.OpenDuration)
		g.halfOpenSuccesses = 0
		return
	}
	g.failures = append(g.failures, now)
	cutoff := now.Add(-time.Minute)
	index := 0
	for index < len(g.failures) && g.failures[index].Before(cutoff) {
		index++
	}
	g.failures = append([]time.Time(nil), g.failures[index:]...)
	if len(g.failures) >= g.options.FailureThreshold {
		g.openUntil = now.Add(g.options.OpenDuration)
		g.halfOpenSuccesses = 0
		g.failures = nil
	}
}

func (g *Guard) resetLocked() {
	g.openUntil = time.Time{}
	g.halfOpenSuccesses = 0
	g.halfOpenProbe = false
	g.failures = nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
