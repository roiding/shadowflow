package proxypool

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

var ErrPrewarmIncomplete = errors.New("proxy prewarm did not reach its target")

type ManagerOptions struct {
	URL            string
	Token          string
	Workers        int
	MaxAttempts    int
	ControlTimeout time.Duration
	AcquireTimeout time.Duration
	Cooldown       time.Duration
	PrewarmCount   int
	PrewarmTimeout time.Duration
	PollInterval   time.Duration
	Protocols      []string
	SourceIDs      []string
	Logger         *slog.Logger
	HTTPClient     *http.Client
	Now            func() time.Time
}

type WarmupStatus struct {
	Target      string     `json:"target"`
	Mode        string     `json:"mode"`
	JobID       string     `json:"job_id,omitempty"`
	State       string     `json:"state"`
	Requested   int        `json:"requested"`
	Minimum     int        `json:"minimum"`
	Ready       int        `json:"ready"`
	Probed      int        `json:"probed"`
	Failed      int        `json:"failed"`
	Reason      string     `json:"reason,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type warmup struct {
	status   WarmupStatus
	done     chan struct{}
	running  bool
	finished time.Time
	err      error
	stale    bool
	waiters  int
	cancel   context.CancelFunc
}

type leaseSummary struct {
	protocol string
	deadline time.Time
	scope    validationScope
}

type Manager struct {
	options  ManagerOptions
	baseURL  string
	client   *http.Client
	now      func() time.Time
	prefix   string
	workers  chan *Worker
	ctx      context.Context
	cancel   context.CancelFunc
	stopOnce sync.Once
	warmWG   sync.WaitGroup
	mu       sync.Mutex
	warmups  map[validationScope]*warmup
	leases   map[string]leaseSummary
	sessions map[string]struct{}
	status   Status
}

func NewManager(options ManagerOptions) (*Manager, error) {
	baseURL, err := normalizeControlURL(options.URL)
	if err != nil {
		return nil, err
	}
	if options.Token == "" {
		return nil, errors.New("a project API key is required for the proxy pool")
	}
	if options.Workers <= 0 {
		options.Workers = 4
	}
	if options.Workers > 4 {
		return nil, errors.New("managed upstream supports at most four workers")
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 3
	}
	if options.ControlTimeout <= 0 {
		options.ControlTimeout = 5 * time.Second
	}
	if options.AcquireTimeout <= 0 {
		options.AcquireTimeout = 30 * time.Second
	}
	if options.Cooldown <= 0 {
		options.Cooldown = 300 * time.Second
	}
	if options.PrewarmCount <= 0 {
		options.PrewarmCount = 8
	}
	if options.PrewarmCount > 100 {
		return nil, errors.New("proxy prewarm count must be between 1 and 100")
	}
	if options.PrewarmTimeout <= 0 {
		options.PrewarmTimeout = 120 * time.Second
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 2 * time.Second
	}
	if len(options.Protocols) == 0 {
		options.Protocols = []string{"http", "socks5"}
	}
	for _, protocol := range options.Protocols {
		if protocol != "http" && protocol != "socks5" {
			return nil, errors.New("managed proxy protocols must be http or socks5")
		}
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	client := options.HTTPClient
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Transport: transport}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var seed [12]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, errors.New("generate proxy worker identity")
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		options: options, baseURL: baseURL, client: &clientCopy, now: options.Now,
		prefix: "shadowflow-" + hex.EncodeToString(seed[:]), workers: make(chan *Worker, options.Workers),
		ctx: ctx, cancel: cancel, warmups: make(map[validationScope]*warmup),
		leases: make(map[string]leaseSummary), sessions: make(map[string]struct{}),
		status: Status{Enabled: true, Strategy: Strategy("sticky"), Workers: options.Workers, MaxAttempts: options.MaxAttempts},
	}
	for workerID := range options.Workers {
		manager.workers <- &Worker{manager: manager, id: workerID, bindings: make(map[validationScope]*binding)}
	}
	return manager, nil
}

func fingerprint(values ...string) string {
	encoded, _ := json.Marshal(values)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (manager *Manager) MaxAttempts() int { return manager.options.MaxAttempts }

func (manager *Manager) EnsurePrepared(ctx context.Context, rawURL string) error {
	scope, err := scopeForURL(rawURL)
	if err != nil {
		return err
	}
	return manager.prepare(ctx, scope, false, 1, 1)
}

func (manager *Manager) RequestContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(manager.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func (manager *Manager) Prepare(ctx context.Context, rawURLs ...string) error {
	return manager.PrepareForRequests(ctx, -1, manager.options.Workers, rawURLs...)
}

func (manager *Manager) preparationSize(requestCount, concurrency int) (int, int) {
	active := min(concurrency, manager.options.Workers, manager.options.PrewarmCount)
	if requestCount >= 0 {
		active = min(active, requestCount)
	}
	target := min(active+(active+1)/2, manager.options.PrewarmCount)
	if requestCount >= 0 {
		target = min(target, requestCount)
	}
	return target, active
}

func (manager *Manager) PrepareForRequests(ctx context.Context, requestCount, concurrency int, rawURLs ...string) error {
	if requestCount < -1 || concurrency < 1 {
		return errors.New("invalid proxy preparation workload")
	}
	if requestCount == 0 {
		return ctx.Err()
	}
	count, minimum := manager.preparationSize(requestCount, concurrency)
	seen := make(map[validationScope]bool)
	for _, rawURL := range rawURLs {
		scope, err := scopeForURL(rawURL)
		if err != nil {
			return err
		}
		if seen[scope] {
			continue
		}
		seen[scope] = true
		if err := manager.prepare(ctx, scope, true, count, minimum); err != nil {
			return err
		}
	}
	return nil
}

func (manager *Manager) prepare(ctx context.Context, scope validationScope, fresh bool, count, minimum int) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if manager.ctx.Err() != nil {
		return errors.New("proxy manager is closed")
	}
	manager.mu.Lock()
	if manager.ctx.Err() != nil {
		manager.mu.Unlock()
		return errors.New("proxy manager is closed")
	}
	previous := manager.warmups[scope]
	if previous != nil && previous.running {
		previous.waiters++
		manager.mu.Unlock()
		if err := manager.waitWarmup(ctx, previous); err != nil {
			return err
		}
		if previous.status.Ready >= minimum {
			return nil
		}
		return manager.prepare(ctx, scope, fresh, count, minimum)
	}
	if previous != nil && !previous.stale && previous.err == nil && previous.status.Ready >= minimum &&
		(!fresh || manager.now().Sub(previous.finished) < 30*time.Second) {
		manager.mu.Unlock()
		return nil
	}
	if previous != nil && previous.err != nil && manager.now().Sub(previous.finished) < manager.options.PollInterval {
		manager.mu.Unlock()
		return previous.err
	}
	jobCtx, cancel := context.WithCancel(manager.ctx)
	current := &warmup{running: true, done: make(chan struct{}), waiters: 1, cancel: cancel, status: WarmupStatus{
		Mode: scope.Mode, Target: scope.Target, State: "PENDING", Requested: count, Minimum: minimum,
	}}
	manager.warmups[scope] = current
	manager.warmWG.Add(1)
	manager.mu.Unlock()
	go func() {
		defer manager.warmWG.Done()
		defer cancel()
		manager.finishWarmup(current, manager.runPrewarm(jobCtx, scope, current))
	}()
	return manager.waitWarmup(ctx, current)
}

func (manager *Manager) waitWarmup(ctx context.Context, current *warmup) error {
	defer func() {
		manager.mu.Lock()
		current.waiters--
		if current.waiters == 0 && current.running {
			current.cancel()
		}
		manager.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-current.done:
		return current.err
	}
}

func (manager *Manager) finishWarmup(current *warmup, err error) {
	manager.mu.Lock()
	current.err, current.finished, current.running = err, manager.now(), false
	finishedAt := current.finished
	manager.status.LastRefreshAt = &finishedAt
	if err != nil {
		manager.status.RefreshErrors++
		manager.status.LastRefreshError = err.Error()
		if current.status.State == "PENDING" || current.status.State == "QUEUED" || current.status.State == "RUNNING" {
			current.status.State = "FAILED"
			if errors.Is(err, context.Canceled) {
				current.status.State = "CANCELLED"
			}
		}
	} else {
		manager.status.RefreshSuccesses++
		manager.status.LastRefreshError = ""
	}
	close(current.done)
	manager.mu.Unlock()
}

func (manager *Manager) runPrewarm(parent context.Context, scope validationScope, current *warmup) (resultErr error) {
	ctx, cancel := context.WithTimeout(parent, manager.options.PrewarmTimeout+2*manager.options.ControlTimeout)
	stop := context.AfterFunc(manager.ctx, cancel)
	defer stop()
	defer cancel()
	body := struct {
		Count      int             `json:"count"`
		Timeout    int64           `json:"timeout"`
		Protocols  []string        `json:"protocols"`
		SourceIDs  []string        `json:"source_ids"`
		Validation validationScope `json:"validation"`
	}{current.status.Requested, durationSeconds(manager.options.PrewarmTimeout), manager.options.Protocols, manager.options.SourceIDs, scope}
	var job prewarmJob
	createErr := manager.control(ctx, http.MethodPost, "/prewarm", body, &job, manager.options.ControlTimeout, http.StatusAccepted)
	if createErr != nil {
		var failure *controlError
		if errors.As(createErr, &failure) && !failure.temporary() {
			return fmt.Errorf("start proxy prewarm: %w", createErr)
		}
		// The public control path can drop connections; one retry keeps a
		// transient blip from aborting the whole collection run. A duplicated
		// create surfaces as a pool conflict on the retry.
		if waitErr := waitContext(ctx, 500*time.Millisecond); waitErr != nil {
			return fmt.Errorf("start proxy prewarm: %w", waitErr)
		}
		if createErr = manager.control(ctx, http.MethodPost, "/prewarm", body, &job, manager.options.ControlTimeout, http.StatusAccepted); createErr != nil {
			return fmt.Errorf("start proxy prewarm: %w", createErr)
		}
	}
	if !safeIdentifier(job.ID) || job.Kind != "prewarm" {
		return errors.New("invalid proxy prewarm job identity")
	}
	jobID := job.ID
	terminal := false
	defer func() {
		if !terminal {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), manager.options.ControlTimeout)
			defer cleanupCancel()
			manager.recordCleanup(manager.control(cleanupCtx, http.MethodPost, "/jobs/"+jobID+"/cancel", struct{}{}, nil,
				manager.options.ControlTimeout, http.StatusOK, http.StatusNotFound))
		}
	}()
	manager.options.Logger.Info("proxy prewarm started", "job_id", jobID, "target", scope.Target, "count", body.Count)
	for {
		if job.ID != jobID || job.Kind != "prewarm" || job.Target < body.Count || job.Ready < 0 {
			return errors.New("proxy prewarm returned inconsistent progress")
		}
		manager.mu.Lock()
		current.status.JobID, current.status.State = jobID, job.State
		current.status.Ready, current.status.Probed, current.status.Failed = job.Ready, job.Probed, job.Failed
		current.status.Reason, current.status.CompletedAt = safeReason(job.StopReason), job.FinishedAt
		manager.mu.Unlock()
		switch job.State {
		case "COMPLETED":
			terminal = true
			if job.Ready < body.Count || job.FinishedAt == nil {
				return fmt.Errorf("%w: ready %d/%d", ErrPrewarmIncomplete, job.Ready, body.Count)
			}
			manager.options.Logger.Info("proxy prewarm completed", "job_id", jobID, "target", scope.Target, "ready", job.Ready)
			return nil
		case "PARTIAL":
			terminal = true
			if job.Ready >= current.status.Minimum && job.FinishedAt != nil {
				manager.options.Logger.Info("proxy prewarm has enough working capacity", "job_id", jobID, "target", scope.Target,
					"ready", job.Ready, "minimum", current.status.Minimum, "requested", body.Count)
				return nil
			}
			return fmt.Errorf("%w: PARTIAL ready %d, minimum %d", ErrPrewarmIncomplete, job.Ready, current.status.Minimum)
		case "FAILED", "CANCELLED", "INTERRUPTED":
			terminal = true
			return fmt.Errorf("%w: %s ready %d/%d (%s)", ErrPrewarmIncomplete, job.State, job.Ready, body.Count, safeReason(job.StopReason))
		case "PENDING", "QUEUED", "RUNNING":
			// A RUNNING job that still reports the original target and already
			// holds ready >= count has the requested validated capacity right
			// now. Waiting for a terminal state would only burn the whole
			// prewarm budget, and a finalize failure pool-side would otherwise
			// stall this run. A raised target means the pool is deliberately
			// topping up beyond the request, so the terminal-state contract
			// still applies there.
			if job.State == "RUNNING" && job.Ready >= body.Count && job.Target <= body.Count {
				terminal = true
				manager.options.Logger.Info("proxy prewarm reached capacity while running",
					"job_id", jobID, "target", scope.Target, "ready", job.Ready)
				return nil
			}
		default:
			return errors.New("proxy prewarm returned an unknown state")
		}
		for {
			if err := waitContext(ctx, manager.options.PollInterval); err != nil {
				return fmt.Errorf("wait for proxy prewarm: %w", err)
			}
			var updated prewarmJob
			err := manager.control(ctx, http.MethodGet, "/jobs/"+jobID, nil, &updated, manager.options.ControlTimeout, http.StatusOK)
			if err == nil {
				job = updated
				break
			}
			var failure *controlError
			if errors.As(err, &failure) && !failure.temporary() {
				return err
			}
		}
	}
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (manager *Manager) Borrow(ctx context.Context, rawURLs ...string) (*Worker, error) {
	var scope validationScope
	if len(rawURLs) > 0 {
		var err error
		scope, err = scopeForURL(rawURLs[0])
		if err != nil {
			return nil, err
		}
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-manager.ctx.Done():
		return nil, errors.New("proxy manager is closed")
	case worker := <-manager.workers:
		if ctx.Err() != nil || manager.ctx.Err() != nil {
			worker.Release()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, manager.ctx.Err()
		}
		if scope.Mode == "" {
			return worker, nil
		}
		available := []*Worker{worker}
		for len(available) < manager.options.Workers {
			select {
			case candidate := <-manager.workers:
				available = append(available, candidate)
			default:
				return manager.preferBoundWorker(available, scope), nil
			}
		}
		return manager.preferBoundWorker(available, scope), nil
	}
}

func (manager *Manager) preferBoundWorker(available []*Worker, scope validationScope) *Worker {
	selected := 0
	now := manager.now()
	for index, candidate := range available {
		current := candidate.bindings[scope]
		if current != nil && !current.failed && current.lastKey != "" && current.expiresAt.After(now) &&
			(current.lease.Proxy.ExpiresAt.IsZero() || current.lease.Proxy.ExpiresAt.After(now)) {
			selected = index
			break
		}
	}
	for index, candidate := range available {
		if index != selected {
			candidate.Release()
		}
	}
	return available[selected]
}

func (manager *Manager) recordCleanup(err error) {
	if err == nil {
		return
	}
	manager.mu.Lock()
	manager.status.CleanupErrors++
	manager.mu.Unlock()
	manager.options.Logger.Warn("proxy cleanup failed", "error", err)
}

func (manager *Manager) Stop() {
	manager.stopOnce.Do(func() {
		manager.mu.Lock()
		manager.cancel()
		manager.mu.Unlock()
		manager.warmWG.Wait()
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 2*manager.options.ControlTimeout)
	workers:
		for range manager.options.Workers {
			select {
			case <-manager.workers:
			case <-drainCtx.Done():
				manager.recordCleanup(errors.New("proxy workers did not stop before cleanup deadline"))
				break workers
			}
		}
		drainCancel()
		manager.mu.Lock()
		leaseIDs, sessionIDs := make([]string, 0, len(manager.leases)), make([]string, 0, len(manager.sessions))
		for leaseID := range manager.leases {
			leaseIDs = append(leaseIDs, leaseID)
		}
		for sessionID := range manager.sessions {
			sessionIDs = append(sessionIDs, sessionID)
		}
		manager.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*manager.options.ControlTimeout)
		defer cancel()
		for _, leaseID := range leaseIDs {
			_ = manager.releaseLease(ctx, leaseID)
		}
		for _, sessionID := range sessionIDs {
			_ = manager.releaseSession(ctx, sessionID)
		}
		manager.client.CloseIdleConnections()
	})
}

func (manager *Manager) Status() Status {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	status := manager.status
	status.BusyWorkers = manager.options.Workers - len(manager.workers)
	status.ByProtocol = make(map[string]int)
	status.ProxyCount = len(manager.leases)
	for _, lease := range manager.leases {
		status.ByProtocol[lease.protocol]++
		if manager.now().Before(lease.deadline) {
			status.AvailableCount++
		} else {
			status.ExpiredCount++
		}
	}
	status.Prewarms = make([]WarmupStatus, 0, len(manager.warmups))
	for _, current := range manager.warmups {
		status.Refreshing = status.Refreshing || current.running
		status.Prewarms = append(status.Prewarms, current.status)
	}
	sort.Slice(status.Prewarms, func(left, right int) bool { return status.Prewarms[left].Target < status.Prewarms[right].Target })
	return status
}
