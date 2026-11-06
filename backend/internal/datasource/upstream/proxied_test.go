package upstream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/roiding/shadowflow/internal/proxypool"
)

type staticProxyPool struct {
	mu        sync.Mutex
	proxies   []proxypool.Proxy
	next      int
	failures  map[string]int
	successes map[string]int
}

func (p *staticProxyPool) MaxAttempts() int     { return len(p.proxies) }
func (p *staticProxyPool) DirectFallback() bool { return false }

func (p *staticProxyPool) Pick(exclude map[string]struct{}) (proxypool.Selection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		if p.next >= len(p.proxies) {
			return proxypool.Selection{}, proxypool.ErrNoProxy
		}
		proxy := p.proxies[p.next]
		key := proxy.Key()
		if _, skipped := exclude[key]; skipped {
			p.next++
			continue
		}
		p.next++
		return proxypool.Selection{Proxy: &proxy, Key: key}, nil
	}
}

func (p *staticProxyPool) ReportSuccess(selected proxypool.Selection) {
	if selected.Proxy == nil {
		return
	}
	p.mu.Lock()
	p.successes[selected.Key]++
	p.mu.Unlock()
}

func (p *staticProxyPool) ReportFailure(selected proxypool.Selection, _ error) {
	if selected.Proxy == nil {
		return
	}
	p.mu.Lock()
	p.failures[selected.Key]++
	p.mu.Unlock()
}

func TestProxiedTransportRetriesDifferentProxiesAndReportsBodySuccess(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("X-Proxy"))
		mu.Unlock()
		if len(seen) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	// A fake proxy transport records the selected proxy, then sends the
	// request directly to the test TLS server.
	transport := &proxyRecordingTransport{target: server.Client().Transport, server: server}
	pool := &staticProxyPool{
		proxies: []proxypool.Proxy{
			{Protocol: "http", Host: "bad.test", Port: 80},
			{Protocol: "http", Host: "good.test", Port: 80},
		},
		failures:  make(map[string]int),
		successes: make(map[string]int),
	}
	client := &http.Client{Transport: NewProxiedTransport(transport, pool)}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("body=%q", body)
	}
	mu.Lock()
	proxies := append([]string(nil), seen...)
	mu.Unlock()
	if len(proxies) != 2 || proxies[0] != "bad.test:80" || proxies[1] != "good.test:80" {
		t.Fatalf("proxies=%v", proxies)
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.failures["http://bad.test:80"] != 1 || pool.successes["http://good.test:80"] != 1 {
		t.Fatalf("failures=%v successes=%v", pool.failures, pool.successes)
	}
}

type proxyRecordingTransport struct {
	target http.RoundTripper
	server *httptest.Server
}

func (t *proxyRecordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	target := request.Header.Get("X-Shadowflow-Target")
	if target == "" {
		return t.target.RoundTrip(request)
	}
	forwarded, err := http.NewRequestWithContext(request.Context(), request.Method, target, nil)
	if err != nil {
		return nil, err
	}
	forwarded.Header.Set("X-Proxy", request.URL.Host)
	return t.target.RoundTrip(forwarded)
}
