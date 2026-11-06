package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"

	"github.com/roiding/shadowflow/internal/proxypool"
)

type ProxyProvider interface {
	MaxAttempts() int
	DirectFallback() bool
	Pick(exclude map[string]struct{}) (proxypool.Selection, error)
	ReportSuccess(proxypool.Selection)
	ReportFailure(proxypool.Selection, error)
}

// ProxiedTransport is a retrying HTTP transport used only for upstream data
// requests. Proxy acquisition happens before each attempt, so a pool refresh
// affects in-flight collection without rebuilding the EastMoney client.
//
// Attempts after the first are safe for these GET requests, but every
// RoundTrip caller must close the returned body before starting new work.
type ProxiedTransport struct {
	pool   ProxyProvider
	routed http.RoundTripper
}

func NewProxiedTransport(base http.RoundTripper, pool ProxyProvider) *ProxiedTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	routed := newRoutedTransport(base)
	return &ProxiedTransport{pool: pool, routed: routed}
}

// newRoutedTransport extracts the selected proxy from the request context.
func newRoutedTransport(base http.RoundTripper) http.RoundTripper {
	if _, ok := base.(*http.Transport); !ok {
		// Custom round trippers are used by tests and callers with their own
		// dial/TLS stack; keep them intact and apply routing at the outer layer.
		return &contextProxyTransport{base: base}
	}
	routed := base.(*http.Transport).Clone()
	routed.Proxy = func(request *http.Request) (*url.URL, error) {
		route, _ := request.Context().Value(proxyRouteKey{}).(proxyRoute)
		if route.proxy == nil {
			return nil, nil
		}
		return route.proxy.URL(), nil
	}
	return routed
}

func (t *ProxiedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("upstream proxy transport received an invalid request")
	}
	if t.pool == nil {
		return t.routed.RoundTrip(request)
	}
	ctx := request.Context()
	excluded := make(map[string]struct{})
	attempts := t.pool.MaxAttempts()
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		selection, err := t.pool.Pick(excluded)
		if err != nil {
			if t.pool.DirectFallback() {
				return t.routed.RoundTrip(cloneRequest(ctx, request, proxyRoute{}))
			}
			if lastErr != nil {
				return nil, fmt.Errorf("proxy pool exhausted after %d attempts: %w", attempt, lastErr)
			}
			return nil, fmt.Errorf("select proxy: %w", err)
		}
		requestCopy := cloneRequest(ctx, request, proxyRoute{proxy: selection.Proxy})
		response, err := t.routed.RoundTrip(requestCopy)
		if err == nil && !retryableStatus(response.StatusCode) {
			if response.Body == http.NoBody {
				t.pool.ReportSuccess(selection)
				return response, nil
			}
			response.Body = &reportingBody{ReadCloser: response.Body, report: func(complete bool) {
				if complete {
					t.pool.ReportSuccess(selection)
				} else {
					t.pool.ReportFailure(selection, io.ErrUnexpectedEOF)
				}
			}}
			return response, nil
		}
		if err == nil {
			err = fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			_ = response.Body.Close()
		} else if response != nil {
			_ = response.Body.Close()
		}
		lastErr = err
		t.pool.ReportFailure(selection, err)
		excluded[selection.Key] = struct{}{}
		if ctx.Err() != nil || selection.Direct {
			break
		}
	}
	return nil, fmt.Errorf("upstream request failed after %d proxy attempts: %w", attempts, lastErr)
}

func cloneRequest(ctx context.Context, request *http.Request, route proxyRoute) *http.Request {
	clone := request.Clone(context.WithValue(ctx, proxyRouteKey{}, route))
	clone.RequestURI = ""
	return clone
}

func retryableStatus(status int) bool {
	return status == http.StatusProxyAuthRequired ||
		status == http.StatusUnauthorized ||
		status == http.StatusForbidden ||
		status == http.StatusRequestTimeout ||
		status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests ||
		status >= http.StatusInternalServerError
}

type reportingBody struct {
	io.ReadCloser
	once   atomic.Bool
	report func(bool)
}

func (b *reportingBody) Read(buffer []byte) (int, error) {
	n, err := b.ReadCloser.Read(buffer)
	if err == io.EOF {
		b.finish(true)
	} else if err != nil {
		b.finish(false)
	}
	return n, err
}

func (b *reportingBody) Close() error {
	err := b.ReadCloser.Close()
	b.finish(false)
	return err
}

func (b *reportingBody) finish(complete bool) {
	if b.once.CompareAndSwap(false, true) {
		b.report(complete)
	}
}

func (t *ProxiedTransport) CloseIdleConnections() {
	if transport, ok := t.routed.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

var _ http.RoundTripper = (*ProxiedTransport)(nil)

type proxyRouteKey struct{}

type proxyRoute struct {
	proxy *proxypool.Proxy
}

type contextProxyTransport struct{ base http.RoundTripper }

func (t *contextProxyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	route, _ := request.Context().Value(proxyRouteKey{}).(proxyRoute)
	if route.proxy == nil {
		return t.base.RoundTrip(request)
	}
	proxied := request.Clone(request.Context())
	proxied.RequestURI = ""
	proxied.Header = request.Header.Clone()
	proxied.URL.Scheme = "http"
	proxied.URL.Host = net.JoinHostPort(route.proxy.Host, strconv.Itoa(route.proxy.Port))
	proxied.Host = proxied.URL.Host
	// Preserve the real target for the fake transport; production *http.Transport
	// gets proxy routing through its Proxy hook instead of URL rewriting.
	proxied.Header.Set("X-Shadowflow-Target", request.URL.String())
	return t.base.RoundTrip(proxied)
}
