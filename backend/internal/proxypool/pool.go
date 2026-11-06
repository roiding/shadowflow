// Package proxypool supplies rotating outbound proxies fetched from an
// operator-controlled HTTP endpoint. It is intentionally independent of the
// quote client so proxies can be rotated without restarting collection.
package proxypool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrNoProxy           = errors.New("proxy pool has no usable proxy")
	ErrRefreshInProgress = errors.New("proxy pool refresh is already running")
)

type Strategy string

const (
	StrategyRoundRobin Strategy = "round_robin"
	StrategyRandom     Strategy = "random"
)

type Options struct {
	URL             string
	Token           string
	RefreshInterval time.Duration
	RequestTimeout  time.Duration
	MaxAttempts     int
	FailureLimit    int
	Cooldown        time.Duration
	DirectFallback  bool
	Protocols       []string
	Strategy        Strategy
	MaxProxies      int
	Logger          *slog.Logger
	HTTPClient      *http.Client
	Now             func() time.Time
}

type Proxy struct {
	Protocol  string
	Host      string
	Port      int
	Username  string
	Password  string
	ExpiresAt time.Time
}

func (p Proxy) URL() *url.URL {
	value := &url.URL{Scheme: p.Protocol, Host: p.Host}
	if p.Port > 0 {
		value.Host = netJoinHostPort(p.Host, p.Port)
	}
	if p.Username != "" {
		value.User = url.UserPassword(p.Username, p.Password)
	}
	return value
}

// Key identifies a route without including credentials, so it is safe to log
// and safe to use as a retry-exclusion key.
func (p Proxy) Key() string {
	return p.Protocol + "://" + netJoinHostPort(p.Host, p.Port)
}

type Status struct {
	Enabled          bool           `json:"enabled"`
	ProxyCount       int            `json:"proxy_count"`
	AvailableCount   int            `json:"available_count"`
	CoolingCount     int            `json:"cooling_count"`
	ExpiredCount     int            `json:"expired_count"`
	ByProtocol       map[string]int `json:"by_protocol"`
	Refreshing       bool           `json:"refreshing"`
	LastRefreshAt    *time.Time     `json:"last_refresh_at,omitempty"`
	LastRefreshError string         `json:"last_refresh_error,omitempty"`
	RefreshSuccesses uint64         `json:"refresh_successes"`
	RefreshErrors    uint64         `json:"refresh_errors"`
	Requests         uint64         `json:"requests"`
	ProxyFailures    uint64         `json:"proxy_failures"`
	DirectFallbacks  uint64         `json:"direct_fallbacks"`
	DirectFallback   bool           `json:"direct_fallback"`
	Strategy         Strategy       `json:"strategy"`
	MaxAttempts      int            `json:"max_attempts"`
	CoolingUntil     *time.Time     `json:"cooling_until,omitempty"`
	Workers          int            `json:"workers,omitempty"`
	BusyWorkers      int            `json:"busy_workers,omitempty"`
	LeaseAcquires    uint64         `json:"lease_acquires,omitempty"`
	LeaseRenewals    uint64         `json:"lease_renewals,omitempty"`
	SessionChanges   uint64         `json:"session_changes,omitempty"`
	CleanupErrors    uint64         `json:"cleanup_errors,omitempty"`
	Prewarms         []WarmupStatus `json:"prewarms,omitempty"`
}

type entry struct {
	proxy       Proxy
	failures    int
	cooldown    time.Time
	successes   uint64
	lastFailure time.Time
	lastUsed    time.Time
}

// Selection describes one route chosen for a request. Proxy is nil when the
// direct fallback is used.
type Selection struct {
	Proxy  *Proxy
	Direct bool
	Key    string
}

type Pool struct {
	options Options
	client  *http.Client
	now     func() time.Time

	mu               sync.Mutex
	entries          []*entry
	next             int
	refreshing       bool
	lastRefreshAt    time.Time
	lastRefreshError string
	refreshSuccesses uint64
	refreshErrors    uint64
	requests         uint64
	proxyFailures    uint64
	directFallbacks  uint64
}

func New(options Options) (*Pool, error) {
	if _, err := url.ParseRequestURI(options.URL); err != nil {
		return nil, fmt.Errorf("proxy pool URL: %w", err)
	}
	parsed, err := url.Parse(options.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("proxy pool URL must be an absolute http(s) URL")
	}
	if options.RefreshInterval <= 0 {
		options.RefreshInterval = 5 * time.Minute
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 5 * time.Second
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 3
	}
	if options.FailureLimit <= 0 {
		options.FailureLimit = 2
	}
	if options.Cooldown <= 0 {
		options.Cooldown = 2 * time.Minute
	}
	if options.MaxProxies <= 0 {
		options.MaxProxies = 500
	}
	if options.Strategy == "" {
		options.Strategy = StrategyRoundRobin
	}
	if options.Strategy != StrategyRoundRobin && options.Strategy != StrategyRandom {
		return nil, fmt.Errorf("unsupported proxy strategy %q", options.Strategy)
	}
	protocols, err := normalizeProtocols(options.Protocols)
	if err != nil {
		return nil, err
	}
	options.Protocols = protocols
	if len(protocols) == 0 {
		return nil, errors.New("proxy pool must allow at least one protocol")
	}
	client := options.HTTPClient
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// The control-plane request must not depend on a proxy obtained from
		// itself. Operators can still provide a custom client for a static
		// bootstrap proxy when their pool API requires one.
		transport.Proxy = nil
		client = &http.Client{Transport: transport, Timeout: options.RequestTimeout}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Pool{options: options, client: client, now: now}, nil
}

func (p *Pool) MaxAttempts() int {
	return p.options.MaxAttempts
}

func (p *Pool) DirectFallback() bool {
	return p.options.DirectFallback
}

// Start refreshes until ctx is canceled. Call Refresh once before Start when
// a request must not race the first control-plane fetch.
func (p *Pool) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(p.options.RefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := p.Refresh(ctx); err != nil && !errors.Is(err, ErrRefreshInProgress) {
					p.logger().Warn("refresh proxy pool", "error", err)
				}
			}
		}
	}()
}

func (p *Pool) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if transport, ok := p.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

func (p *Pool) Refresh(ctx context.Context) error {
	p.mu.Lock()
	if p.refreshing {
		p.mu.Unlock()
		return ErrRefreshInProgress
	}
	p.refreshing = true
	p.mu.Unlock()

	proxies, err := p.fetch(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshing = false
	p.lastRefreshAt = p.now()
	if err != nil {
		p.refreshErrors++
		p.lastRefreshError = err.Error()
		return err
	}
	p.refreshSuccesses++
	p.replaceLocked(proxies)
	p.lastRefreshError = ""
	return nil
}

func (p *Pool) fetch(ctx context.Context) ([]Proxy, error) {
	requestCtx, cancel := context.WithTimeout(ctx, p.options.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, p.options.URL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "shadowflow-proxy-pool/1")
	if token := strings.TrimSpace(p.options.Token); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch proxy list: %w", err)
	}
	defer response.Body.Close()
	body, err := readLimited(response.Body, 8<<20)
	if err != nil {
		return nil, fmt.Errorf("read proxy list: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy pool returned HTTP %d", response.StatusCode)
	}
	proxies, err := ParseResponse(body)
	if err != nil {
		return nil, err
	}
	filtered := make([]Proxy, 0, len(proxies))
	allowed := make(map[string]struct{}, len(p.options.Protocols))
	for _, protocol := range p.options.Protocols {
		allowed[protocol] = struct{}{}
	}
	seen := make(map[string]struct{}, len(proxies))
	for _, proxy := range proxies {
		if _, ok := allowed[proxy.Protocol]; !ok {
			continue
		}
		key := proxy.Key() + "|" + proxy.Username + "|" + proxy.Password
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		filtered = append(filtered, proxy)
		if len(filtered) >= p.options.MaxProxies {
			break
		}
	}
	if len(filtered) == 0 {
		return nil, errors.New("proxy pool response contained no enabled, usable proxies")
	}
	return filtered, nil
}

func (p *Pool) replaceLocked(proxies []Proxy) {
	previous := make(map[string]*entry, len(p.entries))
	for _, item := range p.entries {
		previous[item.proxy.Key()+"|"+item.proxy.Username+"|"+item.proxy.Password] = item
	}
	entries := make([]*entry, 0, len(proxies))
	for _, proxy := range proxies {
		key := proxy.Key() + "|" + proxy.Username + "|" + proxy.Password
		item := &entry{proxy: proxy}
		if old := previous[key]; old != nil {
			item.failures = old.failures
			item.cooldown = old.cooldown
			item.successes = old.successes
			item.lastFailure = old.lastFailure
			item.lastUsed = old.lastUsed
		}
		entries = append(entries, item)
	}
	p.entries = entries
	if p.next >= len(entries) {
		p.next = 0
	}
}

// Pick never returns an expired proxy. When every proxy is cooling it either
// uses the configured direct fallback or retries the proxy whose cooldown ends
// first, which keeps a single-proxy deployment available during recovery.
func (p *Pool) Pick(exclude map[string]struct{}) (Selection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if len(p.entries) == 0 {
		if p.options.DirectFallback {
			p.directFallbacks++
			return Selection{Direct: true, Key: "direct"}, nil
		}
		return Selection{}, ErrNoProxy
	}
	candidates := make([]int, 0, len(p.entries))
	cooling := make([]int, 0, len(p.entries))
	var earliest *entry
	for index, item := range p.entries {
		if !item.proxy.ExpiresAt.IsZero() && !now.Before(item.proxy.ExpiresAt) {
			continue
		}
		if _, skip := exclude[item.proxy.Key()]; skip {
			continue
		}
		if now.Before(item.cooldown) {
			cooling = append(cooling, index)
			if earliest == nil || item.cooldown.Before(earliest.cooldown) {
				earliest = item
			}
			continue
		}
		candidates = append(candidates, index)
	}
	if len(candidates) == 0 {
		if len(cooling) > 0 && !p.options.DirectFallback {
			// A cooled proxy is still preferable to a complete collection
			// outage. Its failure count remains, so a failed probe trips it
			// back into cooldown immediately.
			index := cooling[0]
			for _, candidate := range cooling[1:] {
				if p.entries[candidate].cooldown.Before(p.entries[index].cooldown) {
					index = candidate
				}
			}
			candidates = []int{index}
		} else if p.options.DirectFallback {
			p.directFallbacks++
			return Selection{Direct: true, Key: "direct"}, nil
		} else {
			return Selection{}, ErrNoProxy
		}
	}

	var index int
	if p.options.Strategy == StrategyRandom {
		index = candidates[rand.IntN(len(candidates))]
	} else {
		found := -1
		for offset := range len(p.entries) {
			candidate := (p.next + offset) % len(p.entries)
			for _, allowed := range candidates {
				if candidate == allowed {
					found = candidate
					break
				}
			}
			if found >= 0 {
				break
			}
		}
		if found < 0 {
			found = candidates[0]
		}
		index = found
		p.next = (index + 1) % len(p.entries)
	}
	item := p.entries[index]
	item.lastUsed = now
	proxy := item.proxy
	p.requests++
	return Selection{Proxy: &proxy, Key: proxy.Key()}, nil
}

func (p *Pool) ReportSuccess(selected Selection) {
	if selected.Proxy == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, item := range p.entries {
		if item.proxy.Key() == selected.Proxy.Key() {
			item.failures = 0
			item.cooldown = time.Time{}
			item.successes++
			return
		}
	}
}

func (p *Pool) ReportFailure(selected Selection, _ error) {
	if selected.Proxy == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for _, item := range p.entries {
		if item.proxy.Key() != selected.Proxy.Key() {
			continue
		}
		item.failures++
		item.lastFailure = now
		p.proxyFailures++
		if item.failures >= p.options.FailureLimit {
			item.cooldown = now.Add(p.options.Cooldown)
			item.failures = 0
		}
		return
	}
}

// DisabledStatus is used by API surfaces when the optional pool is not
// configured. Keeping the shape stable makes dashboards and alerts simpler.
func DisabledStatus() Status {
	return Status{Enabled: false, ByProtocol: map[string]int{}}
}

func (p *Pool) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	status := Status{
		Enabled: true, ProxyCount: len(p.entries), ByProtocol: make(map[string]int),
		Refreshing: p.refreshing, RefreshSuccesses: p.refreshSuccesses,
		RefreshErrors: p.refreshErrors, Requests: p.requests,
		ProxyFailures: p.proxyFailures, DirectFallbacks: p.directFallbacks,
		DirectFallback: p.options.DirectFallback, Strategy: p.options.Strategy,
		MaxAttempts: p.options.MaxAttempts, LastRefreshError: p.lastRefreshError,
	}
	if !p.lastRefreshAt.IsZero() {
		value := p.lastRefreshAt
		status.LastRefreshAt = &value
	}
	var earliest time.Time
	for _, item := range p.entries {
		status.ByProtocol[item.proxy.Protocol]++
		if !item.proxy.ExpiresAt.IsZero() && !now.Before(item.proxy.ExpiresAt) {
			status.ExpiredCount++
			continue
		}
		if now.Before(item.cooldown) {
			status.CoolingCount++
			if earliest.IsZero() || item.cooldown.Before(earliest) {
				earliest = item.cooldown
			}
			continue
		}
		status.AvailableCount++
	}
	if !earliest.IsZero() {
		status.CoolingUntil = &earliest
	}
	return status
}

func (p *Pool) logger() *slog.Logger {
	if p.options.Logger != nil {
		return p.options.Logger
	}
	return slog.Default()
}

func normalizeProtocols(values []string) ([]string, error) {
	allowed := map[string]bool{"http": true, "https": true, "socks5": true, "socks5h": true}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if !allowed[value] {
			return nil, fmt.Errorf("unsupported proxy protocol %q", value)
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func netJoinHostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
