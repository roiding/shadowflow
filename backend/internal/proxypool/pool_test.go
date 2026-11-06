package proxypool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRefreshFetchesParsesAndRotates(t *testing.T) {
	var requestPath string
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"proxies":[{"protocol":"http","host":"one.test","port":80},{"protocol":"socks5","host":"two.test","port":1080}]}}`))
	}))
	defer server.Close()

	pool, err := New(Options{URL: server.URL + "/proxies", Token: "token", Protocols: []string{"http", "socks5"}, Strategy: StrategyRoundRobin})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requestPath != "/proxies" || authorization != "Bearer token" {
		t.Fatalf("control request path=%q authorization=%q", requestPath, authorization)
	}
	first, err := pool.Pick(nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pool.Pick(map[string]struct{}{first.Key: {}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Key == second.Key || second.Proxy.Protocol != "socks5" {
		t.Fatalf("expected distinct proxies, got %q and %q", first.Key, second.Key)
	}
	status := pool.Status()
	if !status.Enabled || status.ProxyCount != 2 || status.AvailableCount != 2 || status.RefreshSuccesses != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestFailureCoolDownAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 27, 21, 0, 0, 0, time.UTC)
	pool, err := New(Options{
		URL: "http://pool.test", Protocols: []string{"http"}, FailureLimit: 1, Cooldown: time.Minute, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	proxies := []Proxy{
		{Protocol: "http", Host: "one.test", Port: 80, ExpiresAt: now.Add(-time.Second)},
		{Protocol: "http", Host: "two.test", Port: 80},
	}
	pool.replaceLocked(proxies)
	first, err := pool.Pick(nil)
	if err != nil || first.Proxy.Host != "two.test" {
		t.Fatalf("pick=%+v error=%v", first, err)
	}
	pool.ReportFailure(first, context.Canceled)
	second, err := pool.Pick(nil)
	if err != nil || second.Proxy.Host != "two.test" {
		t.Fatalf("cooling fallback pick=%+v error=%v", second, err)
	}
	pool.ReportSuccess(second)
	status := pool.Status()
	if status.ExpiredCount != 1 || status.CoolingCount != 0 {
		t.Fatalf("unexpected status after recovery: %+v", status)
	}
}

func TestDirectFallback(t *testing.T) {
	pool, err := New(Options{URL: "http://pool.test", Protocols: []string{"http"}, DirectFallback: true})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := pool.Pick(nil)
	if err != nil || !selected.Direct || selected.Proxy != nil {
		t.Fatalf("selection=%+v error=%v", selected, err)
	}
	if status := pool.Status(); status.DirectFallbacks != 1 {
		t.Fatalf("direct fallbacks=%d", status.DirectFallbacks)
	}
}
