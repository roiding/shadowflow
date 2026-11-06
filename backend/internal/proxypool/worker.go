package proxypool

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type binding struct {
	sessionID string
	lease     Lease
	failed    bool
	version   int64
	lastKey   string
	expiresAt time.Time
}

type Lease struct {
	ID        string
	Proxy     Proxy
	Key       string
	ExpiresAt time.Time
	scope     validationScope
}

type Worker struct {
	manager  *Manager
	id       int
	bindings map[validationScope]*binding
	receipt  *warmup
}

func (worker *Worker) Release() {
	worker.manager.workers <- worker
}

func (worker *Worker) Get(ctx context.Context, rawURL string, excluded map[string]struct{}, requestBudget ...time.Duration) (Lease, error) {
	scope, err := scopeForURL(rawURL)
	if err != nil {
		return Lease{}, err
	}
	manager := worker.manager
	requiredRemaining := 5 * time.Second
	if len(requestBudget) > 0 {
		requiredRemaining = max(requiredRemaining, requestBudget[0]+500*time.Millisecond)
	}
	if err := manager.EnsurePrepared(ctx, rawURL); err != nil {
		return Lease{}, err
	}
	manager.mu.Lock()
	worker.receipt = manager.warmups[scope]
	manager.mu.Unlock()
	current := worker.bindings[scope]
	if current == nil {
		current = &binding{sessionID: workerSessionID(manager.prefix, worker.id, scope)}
		worker.bindings[scope] = current
	}
	if _, skip := excluded[current.lease.Key]; skip && current.lease.ID != "" {
		current.failed = true
	}
	if current.failed {
		if err := worker.discard(ctx, current); err != nil {
			return Lease{}, err
		}
	}
	if current.lease.ID != "" && manager.now().Add(requiredRemaining).Before(current.lease.ExpiresAt) {
		manager.mu.Lock()
		manager.status.Requests++
		manager.mu.Unlock()
		return current.lease, nil
	}
	if !current.expiresAt.IsZero() && !manager.now().Add(requiredRemaining).Before(current.expiresAt) ||
		!current.lease.Proxy.ExpiresAt.IsZero() && !manager.now().Add(requiredRemaining).Before(current.lease.Proxy.ExpiresAt) {
		if err := worker.discard(ctx, current); err != nil {
			return Lease{}, err
		}
	}
	if current.lease.ID != "" {
		if err := manager.releaseLease(ctx, current.lease.ID); err != nil {
			return Lease{}, fmt.Errorf("release expiring proxy lease: %w", err)
		}
		current.lease = Lease{}
	}
	body := struct {
		Count      int             `json:"count"`
		Timeout    int64           `json:"timeout"`
		CD         int64           `json:"cd"`
		Mode       string          `json:"mode"`
		SessionID  string          `json:"session_id"`
		Protocols  []string        `json:"protocols"`
		SourceIDs  []string        `json:"source_ids"`
		Validation validationScope `json:"validation"`
	}{1, durationSeconds(manager.options.AcquireTimeout), durationSeconds(manager.options.Cooldown), "sticky", current.sessionID,
		manager.options.Protocols, manager.options.SourceIDs, scope}
	manager.mu.Lock()
	manager.sessions[current.sessionID] = struct{}{}
	manager.mu.Unlock()
	var delivered acquireResponse
	if err := manager.control(ctx, http.MethodPost, "/proxies/acquire", body, &delivered,
		manager.options.AcquireTimeout+manager.options.ControlTimeout, http.StatusOK); err != nil {
		current.failed = true
		return Lease{}, fmt.Errorf("acquire proxy lease: %w", err)
	}
	manager.mu.Lock()
	for _, item := range delivered.Items {
		if safeIdentifier(item.LeaseID) {
			manager.leases[item.LeaseID] = leaseSummary{protocol: item.Connection.Transport, deadline: item.LeaseExpiresAt, scope: scope}
		}
	}
	manager.mu.Unlock()
	if delivered.Requested == 1 && delivered.Delivered == 0 && delivered.Missing == 1 && len(delivered.Items) == 0 {
		return Lease{}, &supplyError{Reason: safeReason(delivered.Reason)}
	}
	lease, err := worker.validateDelivery(delivered, current, scope)
	if err == nil {
		if _, skip := excluded[lease.Key]; skip {
			err = errors.New("proxy pool redelivered a failed connection")
		}
	}
	if err != nil {
		current.failed = true
		cleanupCtx, cancel := context.WithTimeout(context.Background(), manager.options.ControlTimeout)
		defer cancel()
		for _, item := range delivered.Items {
			if safeIdentifier(item.LeaseID) {
				_ = manager.releaseLease(cleanupCtx, item.LeaseID)
			}
		}
		_ = worker.discard(cleanupCtx, current)
		return Lease{}, err
	}
	manager.mu.Lock()
	manager.leases[lease.ID] = leaseSummary{protocol: lease.Proxy.Protocol, deadline: lease.ExpiresAt, scope: scope}
	manager.status.LeaseAcquires++
	manager.status.Requests++
	if !*delivered.SessionChanged {
		manager.status.LeaseRenewals++
	} else if current.version > 0 {
		manager.status.SessionChanges++
	}
	manager.mu.Unlock()
	current.lease, current.version, current.lastKey = lease, delivered.BindingVersion, lease.Key
	current.expiresAt = delivered.BindingExpiresAt
	return lease, nil
}

func (worker *Worker) validateDelivery(delivered acquireResponse, current *binding, scope validationScope) (Lease, error) {
	if delivered.Requested != 1 || delivered.Delivered != 1 || delivered.Missing != 0 || len(delivered.Items) != 1 ||
		delivered.SessionID != current.sessionID || delivered.SessionChanged == nil || delivered.BindingVersion < 1 {
		return Lease{}, errors.New("invalid sticky proxy delivery")
	}
	item := delivered.Items[0]
	if !safeIdentifier(item.LeaseID) || item.SourceID == "" {
		return Lease{}, errors.New("proxy delivery is missing lease or source identity")
	}
	allowedProtocol := false
	for _, protocol := range worker.manager.options.Protocols {
		allowedProtocol = allowedProtocol || item.Connection.Transport == protocol
	}
	if !allowedProtocol || item.Connection.Username != "" && item.Connection.Password == nil {
		return Lease{}, errors.New("proxy delivery has unsupported transport or incomplete authentication")
	}
	if worker.manager.options.SourceIDs != nil {
		allowedSource := false
		for _, sourceID := range worker.manager.options.SourceIDs {
			allowedSource = allowedSource || item.SourceID == sourceID
		}
		if !allowedSource {
			return Lease{}, errors.New("proxy delivery is outside the requested source scope")
		}
	}
	password := ""
	if item.Connection.Password != nil {
		password = *item.Connection.Password
	}
	proxy, err := validateProxy(item.Connection.Transport, item.Connection.Host, item.Connection.Port, item.Connection.Username, password)
	if err != nil {
		return Lease{}, errors.New("proxy delivery contains an invalid connection")
	}
	now := worker.manager.now()
	if item.Validation.Mode != scope.Mode || item.Validation.Target != scope.Target ||
		item.Validation.CompletedAt.IsZero() || item.Validation.CompletedAt.After(now.Add(5*time.Second)) ||
		!item.Validation.ValidUntil.After(now) || !item.Validation.ValidUntil.After(item.Validation.CompletedAt) {
		return Lease{}, errors.New("proxy delivery has stale or mismatched validation evidence")
	}
	deadline := item.LeaseExpiresAt
	if delivered.BindingExpiresAt.IsZero() || delivered.BindingExpiresAt.Before(deadline) {
		deadline = delivered.BindingExpiresAt
	}
	if item.ProviderExpiresAt != nil {
		proxy.ExpiresAt = *item.ProviderExpiresAt
		if item.ProviderExpiresAt.Before(deadline) {
			deadline = *item.ProviderExpiresAt
		}
	}
	if !deadline.After(now.Add(time.Second)) {
		return Lease{}, errors.New("proxy delivery expires before it can be used")
	}
	key := fingerprint(proxy.Protocol, proxy.Host, strconv.Itoa(proxy.Port), proxy.Username, proxy.Password)
	if !*delivered.SessionChanged && current.lastKey != "" && (key != current.lastKey || delivered.BindingVersion != current.version) {
		return Lease{}, errors.New("proxy binding changed without session_changed")
	}
	return Lease{ID: item.LeaseID, Proxy: proxy, Key: key, ExpiresAt: deadline, scope: scope}, nil
}

func (worker *Worker) Fail(ctx context.Context, lease Lease, reason string) error {
	current := worker.bindings[lease.scope]
	if current == nil || current.lease.ID != lease.ID {
		return errors.New("proxy failure does not match the worker lease")
	}
	current.failed = true
	manager := worker.manager
	manager.mu.Lock()
	manager.status.ProxyFailures++
	manager.mu.Unlock()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*manager.options.ControlTimeout)
	defer cancel()
	body := struct {
		Outcome string `json:"outcome"`
		Stage   string `json:"stage"`
		Note    string `json:"note"`
	}{"failure", "upstream_http", safeReason(reason)}
	manager.recordCleanup(manager.control(cleanupCtx, http.MethodPost, "/leases/"+lease.ID+"/feedback", body, nil,
		manager.options.ControlTimeout, http.StatusOK, http.StatusNoContent, http.StatusNotFound))
	return worker.discard(cleanupCtx, current)
}

func (worker *Worker) Retire(ctx context.Context, lease Lease) error {
	current := worker.bindings[lease.scope]
	if current == nil || current.lease.ID != lease.ID {
		return errors.New("expiring lease does not match the worker binding")
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*worker.manager.options.ControlTimeout)
	defer cancel()
	return worker.discard(cleanupCtx, current)
}

func (worker *Worker) discard(ctx context.Context, current *binding) error {
	var releaseErr error
	if current.lease.ID != "" {
		releaseErr = worker.manager.releaseLease(ctx, current.lease.ID)
		if releaseErr == nil {
			current.lease = Lease{}
		}
	}
	sessionErr := worker.manager.releaseSession(ctx, current.sessionID)
	if err := errors.Join(releaseErr, sessionErr); err != nil {
		current.failed = true
		return fmt.Errorf("release failed proxy binding: %w", err)
	}
	current.failed = false
	current.expiresAt = time.Time{}
	return nil
}

func (worker *Worker) Replenish(ctx context.Context, rawURL string) error {
	scope, err := scopeForURL(rawURL)
	if err != nil {
		return err
	}
	manager := worker.manager
	now := manager.now()
	manager.mu.Lock()
	count := 1
	if worker.receipt != nil {
		count = worker.receipt.status.Requested
	}
	for _, lease := range manager.leases {
		if lease.scope == scope && lease.deadline.After(now.Add(5*time.Second)) {
			count--
		}
	}
	count = max(1, count)
	if current := manager.warmups[scope]; current != nil && current == worker.receipt && !current.running {
		current.stale = true
	}
	manager.mu.Unlock()
	return manager.prepare(ctx, scope, true, count, 1)
}
