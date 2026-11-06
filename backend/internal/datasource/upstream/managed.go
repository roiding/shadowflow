package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/roiding/shadowflow/internal/proxypool"
)

var errLeaseBudgetExpired = errors.New("proxy lease expired before the response completed")

type ManagedTransport struct {
	manager        *proxypool.Manager
	routed         http.RoundTripper
	attemptTimeout time.Duration
	mu             sync.Mutex
	backoff        map[string]time.Time
}

func NewManagedTransport(base http.RoundTripper, manager *proxypool.Manager, attemptTimeout time.Duration) *ManagedTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	if attemptTimeout <= 0 {
		attemptTimeout = 12 * time.Second
	}
	return &ManagedTransport{manager: manager, routed: newRoutedTransport(base), attemptTimeout: attemptTimeout, backoff: make(map[string]time.Time)}
}

func (transport *ManagedTransport) Prepare(ctx context.Context, rawURLs ...string) error {
	return transport.manager.Prepare(ctx, rawURLs...)
}

func (transport *ManagedTransport) PrepareForRequests(ctx context.Context, requestCount, concurrency int, rawURLs ...string) error {
	return transport.manager.PrepareForRequests(ctx, requestCount, concurrency, rawURLs...)
}

func (transport *ManagedTransport) EnsurePrepared(ctx context.Context, rawURL string) error {
	return transport.manager.EnsurePrepared(ctx, rawURL)
}

func (transport *ManagedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("invalid managed upstream request")
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		return nil, errors.New("managed upstream supports only idempotent GET and HEAD requests")
	}
	ctx, cancel := transport.manager.RequestContext(request.Context())
	defer cancel()
	request = request.Clone(ctx)
	if err := transport.manager.EnsurePrepared(ctx, request.URL.String()); err != nil {
		return nil, err
	}
	worker, err := transport.manager.Borrow(ctx, request.URL.String())
	if err != nil {
		return nil, err
	}
	defer worker.Release()
	excluded := make(map[string]struct{})
	replenished := false
	var lastErr error
	for attempt := 0; attempt < transport.manager.MaxAttempts(); attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err := transport.waitBackoff(ctx, request.URL.Host); err != nil {
			return nil, err
		}
		lease, err := worker.Get(ctx, request.URL.String(), excluded, transport.attemptTimeout)
		if err != nil {
			lastErr = err
			if errors.Is(err, proxypool.ErrNoProxy) && !replenished && attempt+1 < transport.manager.MaxAttempts() {
				replenished = true
				if err := worker.Replenish(ctx, request.URL.String()); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("obtain upstream proxy: %w: %w", proxypool.ErrControlPlane, err)
		}
		response, reason, retryDelay, err := transport.attempt(request, lease)
		if err == nil {
			return response, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = err
		excluded[lease.Key] = struct{}{}
		var cleanupErr error
		if errors.Is(err, errLeaseBudgetExpired) {
			cleanupErr = worker.Retire(ctx, lease)
		} else {
			cleanupErr = worker.Fail(ctx, lease, reason)
		}
		if cleanupErr != nil {
			return nil, errors.Join(lastErr, cleanupErr)
		}
		transport.CloseIdleConnections()
		if attempt+1 < transport.manager.MaxAttempts() {
			if retryDelay < time.Duration(attempt+1)*250*time.Millisecond {
				retryDelay = time.Duration(attempt+1) * 250 * time.Millisecond
			}
			if err := waitManagedRetry(ctx, retryDelay); err != nil {
				return nil, err
			}
		}
	}
	return nil, fmt.Errorf("upstream exhausted %d proxy attempts: %w", transport.manager.MaxAttempts(), lastErr)
}

func (transport *ManagedTransport) attempt(request *http.Request, lease proxypool.Lease) (*http.Response, string, time.Duration, error) {
	deadline := time.Now().Add(transport.attemptTimeout)
	leaseLimited := false
	if leaseDeadline := lease.ExpiresAt.Add(-250 * time.Millisecond); leaseDeadline.Before(deadline) {
		deadline = leaseDeadline
		leaseLimited = true
	}
	ctx, cancel := context.WithDeadline(request.Context(), deadline)
	defer cancel()
	response, err := transport.routed.RoundTrip(cloneRequest(ctx, request, proxyRoute{proxy: &lease.Proxy}))
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if leaseLimited && errors.Is(ctx.Err(), context.DeadlineExceeded) && request.Context().Err() == nil {
			return nil, "lease_expired", 0, errLeaseBudgetExpired
		}
		reason := "transport_failure"
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			reason = "transport_timeout"
		}
		return nil, reason, 0, fmt.Errorf("upstream proxy %s", reason)
	}
	defer response.Body.Close()
	if retryableStatus(response.StatusCode) {
		reason := "http_" + strconv.Itoa(response.StatusCode)
		delay := retryAfter(response.Header.Get("Retry-After"))
		transport.mu.Lock()
		if until := time.Now().Add(delay); until.After(transport.backoff[request.URL.Host]) {
			transport.backoff[request.URL.Host] = until
		}
		transport.mu.Unlock()
		return nil, reason, delay, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	const limit = 8 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		if leaseLimited && errors.Is(ctx.Err(), context.DeadlineExceeded) && request.Context().Err() == nil {
			return nil, "lease_expired", 0, errLeaseBudgetExpired
		}
		return nil, "truncated_body", 0, errors.New("upstream response body was interrupted")
	}
	if len(body) > limit {
		return nil, "oversized_body", 0, errors.New("upstream response body exceeded its size limit")
	}
	if response.StatusCode == http.StatusOK && request.Method != http.MethodHead {
		if len(bytes.TrimSpace(body)) == 0 {
			return nil, "empty_body", 0, errors.New("upstream returned an empty response")
		}
		if strings.HasPrefix(request.URL.Path, "/api/qt/") && request.URL.Query().Get("cb") == "" && !json.Valid(body) {
			return nil, "invalid_json", 0, errors.New("upstream quote response was not JSON")
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Request = request
	return response, "", 0, nil
}

func retryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(deadline))
	}
	return 0
}

func waitManagedRetry(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (transport *ManagedTransport) waitBackoff(ctx context.Context, host string) error {
	for {
		transport.mu.Lock()
		until := transport.backoff[host]
		transport.mu.Unlock()
		if delay := time.Until(until); delay > 0 {
			if err := waitManagedRetry(ctx, delay); err != nil {
				return err
			}
		} else {
			return nil
		}
	}
}

func (transport *ManagedTransport) CloseIdleConnections() {
	if closer, ok := transport.routed.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
