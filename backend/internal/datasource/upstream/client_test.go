package upstream

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/roiding/shadowflow/internal/config"
)

func TestNewClientWithoutProxyPoolUsesDirectTransport(t *testing.T) {
	result, err := NewClient(context.Background(), config.Config{
		UpstreamMaxConcurrency: 2, UpstreamRatePerSecond: 10, RequestTimeout: 5_000_000_000,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if result.Pool != nil || result.Client == nil || result.Guard == nil {
		t.Fatalf("unexpected client result: %+v", result)
	}
}

func TestNewClientWithProxyPoolDefersNetworkUntilPreparation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	result, err := NewClient(context.Background(), config.Config{
		UpstreamMaxConcurrency: 2, UpstreamRatePerSecond: 10, RequestTimeout: 5_000_000_000,
		ProxyPoolURL: server.URL, ProxyPoolToken: "token", ProxyPoolRefreshEvery: 60_000_000_000,
		ProxyPoolRequestTimeout: 5_000_000_000, ProxyPoolMaxAttempts: 3, ProxyPoolFailureLimit: 2,
		ProxyPoolCooldown: 120_000_000_000, ProxyPoolProtocols: []string{"http"},
		ProxyPoolStrategy: "round_robin", ProxyPoolMaxProxies: 10,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer result.Pool.Stop()
	if result.Pool == nil || result.Client == nil || result.Guard == nil {
		t.Fatalf("unexpected client result: %+v", result)
	}
	status := result.Pool.Status()
	if status.ProxyCount != 0 || status.RefreshSuccesses != 0 || !status.Enabled || status.Workers != 2 || requests != 0 {
		t.Fatalf("unexpected proxy status: %+v", status)
	}
	if result.Client.Timeout != 0 {
		t.Fatal("business timeout must not include prewarm and proxy acquisition")
	}
}
