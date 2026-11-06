package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/roiding/shadowflow/internal/config"
	"github.com/roiding/shadowflow/internal/proxypool"
)

type ClientResult struct {
	Client *http.Client
	Guard  *Guard
	Pool   *proxypool.Manager
}

// NewClient applies the shared concurrency, rate and proxy configuration. The
// proxy control-plane request uses a separate direct client, so it can never
// try to obtain its own proxy list through this pool.
func NewClient(ctx context.Context, cfg config.Config, logger *slog.Logger) (ClientResult, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = cfg.UpstreamMaxConcurrency * 4
	transport.MaxIdleConnsPerHost = cfg.UpstreamMaxConcurrency
	client := &http.Client{Transport: transport, Timeout: cfg.RequestTimeout}
	result := ClientResult{Client: client, Guard: New(client, Options{
		MaxConcurrency: cfg.UpstreamMaxConcurrency, RatePerSecond: cfg.UpstreamRatePerSecond,
	})}
	if cfg.ProxyPoolURL == "" {
		return result, nil
	}

	if cfg.ProxyPoolDirectFallback {
		return ClientResult{}, fmt.Errorf("managed proxy collection requires SHADOWFLOW_PROXY_POOL_DIRECT_FALLBACK=false")
	}
	pool, err := proxypool.NewManager(proxypool.ManagerOptions{
		URL: cfg.ProxyPoolURL, Token: cfg.ProxyPoolToken,
		Workers: min(4, cfg.UpstreamMaxConcurrency), MaxAttempts: cfg.ProxyPoolMaxAttempts,
		ControlTimeout: cfg.ProxyPoolRequestTimeout, AcquireTimeout: cfg.ProxyPoolAcquireTimeout,
		Cooldown: cfg.ProxyPoolCooldown, Protocols: cfg.ProxyPoolProtocols, SourceIDs: cfg.ProxyPoolSourceIDs,
		PrewarmCount: cfg.ProxyPoolPrewarmCount, PrewarmTimeout: cfg.ProxyPoolPrewarmTimeout,
		PollInterval: cfg.ProxyPoolPollEvery, Logger: logger,
	})
	if err != nil {
		return ClientResult{}, fmt.Errorf("create proxy pool: %w", err)
	}
	result.Pool = pool
	result.Client = &http.Client{
		Transport:     NewManagedTransport(transport, pool, cfg.RequestTimeout),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	result.Guard = New(result.Client, Options{
		MaxConcurrency: min(4, cfg.UpstreamMaxConcurrency), RatePerSecond: cfg.UpstreamRatePerSecond,
	})
	return result, nil
}
