package thsauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultRequestTimeout = 20 * time.Second
	defaultRefreshTimeout = 45 * time.Second
	defaultCooldown       = 5 * time.Minute
	maxAuthBodyBytes      = 8 << 20
	maxErrorBodyBytes     = 64 << 10

	dataqHost     = "dataq.10jqka.com.cn"
	dataHost      = "data.10jqka.com.cn"
	ozoneHost     = "ozone.10jqka.com.cn"
	dataqPath     = "/fetch-data-server/fetch/v1/specific_data"
	chartPath     = "/mobileapi/hotspot_focus/chart_config/v1/query_config"
	dataPermPath  = "/mobileapi/hotspot_focus/permission/v1/query"
	ozonePermPath = "/open/api/draw_lots/v1/permission/query"
)

type Manager struct {
	options Options

	rootContext context.Context
	rootCancel  context.CancelFunc

	refreshMutex sync.Mutex
	refresh      *refreshFlight

	stateMutex    sync.Mutex
	state         string
	lastErrorCode string
	lastFailure   *LoginFailure
	retryAt       *time.Time
	lastRefreshAt *time.Time
	refreshCount  int
	stopped       bool
	closed        bool

	sessionMutex      sync.RWMutex
	sessionGeneration uint64
	session           SessionStore
	sessionDigest     [sha256.Size]byte
	jar               *jarState
	client            *http.Client
}

type jarState struct {
	jar      *trackedCookieJar
	revision uint64
}

type refreshFlight struct {
	done chan struct{}
	err  error
}

func New(options Options) (*Manager, error) {
	if options.Transport == nil {
		options.Transport = http.DefaultTransport
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = defaultRequestTimeout
	}
	if options.RefreshTimeout <= 0 {
		options.RefreshTimeout = defaultRefreshTimeout
	}
	if options.Cooldown <= 0 {
		options.Cooldown = defaultCooldown
	}
	if options.SessionFile != "" && !filepathIsAbsolute(options.SessionFile) {
		return nil, fmt.Errorf("%w: session path must be absolute", ErrUnavailable)
	}
	session := options.Session
	if session == nil && options.SessionFile != "" {
		session = fileSessionStore{path: options.SessionFile}
	}
	manager := &Manager{options: options, session: session, state: StateUnconfigured}
	manager.rootContext, manager.rootCancel = context.WithCancel(context.Background())
	jar, err := newCookieJar()
	if err != nil {
		manager.rootCancel()
		return nil, fmt.Errorf("%w: initialize session jar", ErrUnavailable)
	}
	state := &jarState{jar: jar}
	if session != nil {
		cookies, present, loadError := session.LoadSession(context.Background())
		switch {
		case loadError != nil:
			manager.state = StateActionNeeded
			manager.lastErrorCode = "authentication"
			manager.stopped = true
		case present:
			digest, digestError := sessionDigest(cookies, true)
			if installError := installCookies(jar, cookies); installError != nil || digestError != nil {
				manager.state = StateActionNeeded
				manager.lastErrorCode = "authentication"
				manager.stopped = true
			} else {
				manager.sessionDigest = digest
				manager.state = StateExpired
				if hasUsableCookies(snapshotJarCookies(jar)) {
					manager.state = StateReady
				}
				_, state.revision = jar.snapshot()
			}
		}
	}
	manager.jar = state
	manager.client = &http.Client{
		Transport:     options.Transport,
		Timeout:       options.RequestTimeout,
		Jar:           jar,
		CheckRedirect: rejectRedirect,
	}
	return manager, nil
}

func (manager *Manager) Do(request *http.Request) (*http.Response, error) {
	if manager == nil || request == nil {
		return nil, fmt.Errorf("%w: invalid request", ErrUnavailable)
	}
	if targetError := validateTarget(request); targetError != nil {
		return nil, targetError
	}
	if manager.isClosed() {
		return nil, fmt.Errorf("%w: manager closed", ErrUnavailable)
	}
	if request.Method == http.MethodPost && request.GetBody == nil {
		return nil, fmt.Errorf("%w: POST body is not replayable", ErrUnavailable)
	}
	if request.Body != nil {
		defer request.Body.Close()
	}
	request = request.Clone(request.Context())
	request.Header.Del("Cookie")
	request.Header.Del("Authorization")
	request.Header.Del("Proxy-Authorization")
	if _, reloadError := manager.reloadSession(request.Context()); reloadError != nil {
		return nil, reloadError
	}
	generation, client := manager.currentClient()
	response, responseError := manager.send(client, request)
	if responseError != nil {
		return response, responseError
	}
	expired, authError := responseAuthExpired(request, response)
	if authError != nil {
		discardResponse(response)
		return nil, authError
	}
	if !expired {
		return response, nil
	}
	discardResponse(response)
	retryResponse, refreshError := manager.refreshAndRetry(request, generation)
	if refreshError != nil {
		return nil, refreshError
	}
	return retryResponse, nil
}

func (manager *Manager) Login(ctx context.Context) error {
	if manager == nil {
		return ErrUnconfigured
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return manager.authenticate(ctx, nil, true)
}

func (manager *Manager) Import(ctx context.Context, origin string, cookies []*http.Cookie) error {
	if manager == nil {
		return fmt.Errorf("%w: missing manager", ErrUnavailable)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if manager.isClosed() {
		return fmt.Errorf("%w: manager closed", ErrUnavailable)
	}
	originURL, originError := importOrigin(origin)
	if originError != nil {
		return originError
	}
	if len(cookies) == 0 || len(cookies) > maxCookieCount {
		return fmt.Errorf("%w: import requires a bounded cookie set", ErrCredentials)
	}
	candidateJar, err := newCookieJar()
	if err != nil {
		return fmt.Errorf("%w: initialize session jar", ErrUnavailable)
	}
	for _, cookie := range cookies {
		if cookie == nil {
			continue
		}
		cloned := cloneCookie(cookie)
		if cloned.Path == "" {
			cloned.Path = "/"
		}
		validated := cloneCookie(cloned)
		if validated.Domain == "" {
			validated.Domain = strings.ToLower(originURL.Hostname())
		}
		if validateError := validateCookie(validated); validateError != nil {
			return validateError
		}
		candidateJar.SetCookies(originURL, []*http.Cookie{cloned})
	}
	if verifyError := manager.verifyCandidate(ctx, candidateJar); verifyError != nil {
		manager.recordFailure(verifyError)
		return verifyError
	}
	manager.refreshMutex.Lock()
	defer manager.refreshMutex.Unlock()
	if manager.refresh != nil || manager.isClosed() || ctx.Err() != nil {
		return fmt.Errorf("%w: session update already in progress or canceled", ErrUnavailable)
	}
	if publishError := manager.publish(ctx, candidateJar); publishError != nil {
		manager.recordFailure(publishError)
		return publishError
	}
	manager.recordSuccess()
	return nil
}

func (manager *Manager) Status() Status {
	if manager == nil {
		return Status{State: StateUnconfigured}
	}
	manager.refreshMutex.Lock()
	refreshing := manager.refresh != nil
	manager.refreshMutex.Unlock()
	manager.stateMutex.Lock()
	state := manager.state
	switch {
	case manager.closed:
		state = StateUnconfigured
	case refreshing:
		state = StateRefreshing
	}
	status := Status{
		State:         state,
		AutoLogin:     manager.options.AutoLogin,
		LastRefreshAt: cloneTime(manager.lastRefreshAt),
		RefreshCount:  manager.refreshCount,
		RetryAt:       cloneTime(manager.retryAt),
		LastErrorCode: manager.lastErrorCode,
		LastFailure:   cloneLoginFailure(manager.lastFailure),
	}
	manager.stateMutex.Unlock()
	status.ExpiresAt = manager.expiry()
	return status
}

func (manager *Manager) Close() error {
	if manager == nil {
		return nil
	}
	manager.stateMutex.Lock()
	if manager.closed {
		manager.stateMutex.Unlock()
		return nil
	}
	manager.closed = true
	manager.stateMutex.Unlock()
	manager.rootCancel()
	manager.refreshMutex.Lock()
	flight := manager.refresh
	manager.refreshMutex.Unlock()
	if flight != nil {
		<-flight.done
	}
	if closer, ok := manager.options.Transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	return nil
}

func (manager *Manager) authenticate(ctx context.Context, generation *uint64, explicit bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if manager.isClosed() {
		return fmt.Errorf("%w: manager closed", ErrUnavailable)
	}
	manager.refreshMutex.Lock()
	if generation != nil {
		currentGeneration, _ := manager.currentClient()
		if currentGeneration != *generation {
			manager.refreshMutex.Unlock()
			return nil
		}
	}
	if manager.refresh != nil {
		flight := manager.refresh
		manager.refreshMutex.Unlock()
		select {
		case <-flight.done:
			return flight.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if manager.options.Login == nil {
		manager.refreshMutex.Unlock()
		manager.recordFailure(ErrUnconfigured)
		return ErrUnconfigured
	}
	manager.stateMutex.Lock()
	var blocked error
	switch {
	case manager.closed:
		blocked = ErrUnavailable
	case !explicit && manager.stopped:
		blocked = ErrChallenge
		if manager.lastErrorCode == "credentials" {
			blocked = ErrCredentials
		}
	case !explicit && manager.retryAt != nil && time.Now().Before(*manager.retryAt):
		blocked = ErrCooldown
	}
	if blocked != nil {
		manager.stateMutex.Unlock()
		manager.refreshMutex.Unlock()
		return blocked
	}
	manager.stopped = false
	manager.retryAt = nil
	manager.state = StateRefreshing
	manager.stateMutex.Unlock()
	flight := &refreshFlight{done: make(chan struct{})}
	manager.refresh = flight
	manager.refreshMutex.Unlock()

	refreshContext, cancelRefresh := context.WithTimeout(manager.rootContext, manager.options.RefreshTimeout)
	go func() {
		defer cancelRefresh()
		err := manager.performRefresh(refreshContext)
		manager.refreshMutex.Lock()
		flight.err = err
		manager.refresh = nil
		close(flight.done)
		manager.refreshMutex.Unlock()
	}()
	select {
	case <-flight.done:
		return flight.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) performRefresh(ctx context.Context) error {
	candidateJar, err := newCookieJar()
	if err != nil {
		manager.recordFailure(ErrUnavailable)
		return ErrUnavailable
	}
	loginClient := &http.Client{
		Transport:     manager.options.Transport,
		Timeout:       manager.options.RefreshTimeout,
		Jar:           candidateJar,
		CheckRedirect: rejectRedirect,
	}
	if loginError := manager.options.Login(ctx, loginClient); loginError != nil {
		manager.recordFailure(loginError)
		return loginError
	}
	if !hasCandidateCookies(candidateJar) {
		manager.recordFailure(ErrAuthExpired)
		return ErrAuthExpired
	}
	if verifyError := manager.verifyCandidate(ctx, candidateJar); verifyError != nil {
		manager.recordFailure(verifyError)
		return verifyError
	}
	if ctx.Err() != nil || manager.isClosed() {
		return context.Canceled
	}
	if publishError := manager.publish(ctx, candidateJar); publishError != nil {
		manager.recordFailure(publishError)
		return publishError
	}
	manager.recordSuccess()
	return nil
}

func (manager *Manager) verifyCandidate(ctx context.Context, jar http.CookieJar) error {
	request, requestError := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://"+dataHost+chartPath, nil)
	if requestError != nil {
		return ErrUnavailable
	}
	request.Header.Set("Accept", "application/json")
	client := &http.Client{
		Transport:     manager.options.Transport,
		Timeout:       manager.options.RefreshTimeout,
		Jar:           jar,
		CheckRedirect: rejectRedirect,
	}
	response, responseError := client.Do(request)
	if responseError != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: verify session", ErrUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		discardResponse(response)
		return ErrAuthExpired
	}
	if response.StatusCode == http.StatusForbidden {
		discardResponse(response)
		return ErrPermission
	}
	if response.StatusCode == http.StatusTooManyRequests {
		discardResponse(response)
		return ErrChallenge
	}
	if response.StatusCode != http.StatusOK {
		discardResponse(response)
		return fmt.Errorf("%w: verify session HTTP %d", ErrUnavailable, response.StatusCode)
	}
	if response.ContentLength > maxAuthBodyBytes {
		return fmt.Errorf("%w: verify response exceeds size limit", ErrUnavailable)
	}
	body, readError := io.ReadAll(io.LimitReader(response.Body, maxAuthBodyBytes+1))
	if readError != nil || len(body) > maxAuthBodyBytes {
		return fmt.Errorf("%w: verify session response", ErrUnavailable)
	}
	var envelope struct {
		Status json.RawMessage `json:"status_code"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return fmt.Errorf("%w: verify session response", ErrUnavailable)
	}
	status, statusError := parseStatus(envelope.Status)
	if statusError != nil || status != 0 || len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) {
		return ErrAuthExpired
	}
	return nil
}

func (manager *Manager) publish(ctx context.Context, candidate http.CookieJar) error {
	cookies := snapshotJarCookies(candidate)
	defer scrubCookies(cookies)
	if !hasUsableCookies(cookies) {
		return ErrAuthExpired
	}
	installedJar, tracked := candidate.(*trackedCookieJar)
	if !tracked {
		return fmt.Errorf("%w: session cookie metadata is missing", ErrUnavailable)
	}
	manager.sessionMutex.Lock()
	defer manager.sessionMutex.Unlock()
	if manager.isClosed() {
		return context.Canceled
	}
	if manager.session != nil {
		if writeError := manager.saveSessionLocked(ctx, cookies); writeError != nil {
			return writeError
		}
	}
	manager.installSessionLocked(installedJar)
	return nil
}

func (manager *Manager) refreshAndRetry(request *http.Request, generation uint64) (*http.Response, error) {
	if _, reloadError := manager.reloadSession(request.Context()); reloadError != nil {
		return nil, reloadError
	}
	if currentGeneration, _ := manager.currentClient(); currentGeneration == generation {
		if !manager.options.AutoLogin {
			manager.recordFailure(ErrAuthExpired)
			return nil, ErrAuthExpired
		}
		if refreshError := manager.authenticate(request.Context(), &generation, false); refreshError != nil {
			return nil, refreshError
		}
	}
	currentGeneration, client := manager.currentClient()
	if currentGeneration == generation {
		manager.recordFailure(ErrAuthExpired)
		return nil, ErrAuthExpired
	}
	retry, retryError := cloneRequest(request)
	if retryError != nil {
		return nil, retryError
	}
	response, err := manager.send(client, retry)
	if err != nil {
		return nil, err
	}
	expired, authError := responseAuthExpired(retry, response)
	if authError != nil {
		discardResponse(response)
		return nil, authError
	}
	if expired {
		discardResponse(response)
		manager.recordFailure(ErrAuthExpired)
		return nil, ErrAuthExpired
	}
	return response, err
}

func (manager *Manager) send(client *http.Client, request *http.Request) (*http.Response, error) {
	requestCopy, requestError := cloneRequest(request)
	if requestError != nil {
		return nil, requestError
	}
	ctx, cancel := context.WithCancel(request.Context())
	stop := context.AfterFunc(manager.rootContext, cancel)
	finish := func() {
		stop()
		cancel()
	}
	requestCopy = requestCopy.WithContext(ctx)
	response, responseError := client.Do(requestCopy)
	if responseError != nil {
		finish()
		if request.Context().Err() != nil {
			return nil, request.Context().Err()
		}
		return nil, &requestFailure{cause: responseError}
	}
	if response == nil || response.Body == nil {
		finish()
		return nil, fmt.Errorf("%w: empty HTTP response", ErrUnavailable)
	}
	response.Body = &sessionResponseBody{ReadCloser: response.Body, finish: finish}
	if err := manager.persistRotation(request.Context(), client); err != nil {
		discardResponse(response)
		manager.recordFailure(err)
		return nil, err
	}
	response.Request = request
	return response, nil
}

type requestFailure struct {
	cause error
}

func (failure *requestFailure) Error() string {
	return ErrUnavailable.Error() + ": data request failed"
}

func (failure *requestFailure) Unwrap() error {
	return failure.cause
}

func (failure *requestFailure) Is(target error) bool {
	return target == ErrUnavailable
}

func (manager *Manager) currentClient() (uint64, *http.Client) {
	manager.sessionMutex.RLock()
	defer manager.sessionMutex.RUnlock()
	return manager.sessionGeneration, manager.client
}

func (manager *Manager) persistRotation(ctx context.Context, client *http.Client) error {
	ctx, cancel := context.WithTimeout(ctx, manager.options.RequestTimeout)
	stop := context.AfterFunc(manager.rootContext, cancel)
	defer cancel()
	defer stop()
	manager.refreshMutex.Lock()
	defer manager.refreshMutex.Unlock()
	manager.sessionMutex.Lock()
	defer manager.sessionMutex.Unlock()
	if manager.client != client {
		return nil
	}
	if manager.refresh != nil {
		return nil
	}
	cookies, revision := manager.jar.jar.snapshot()
	defer scrubCookies(cookies)
	if revision == manager.jar.revision {
		return nil
	}
	if manager.session != nil {
		if changed, err := manager.reloadSessionLocked(ctx); err != nil || changed {
			return err
		}
		if err := manager.saveSessionLocked(ctx, cookies); err != nil {
			return err
		}
	}
	manager.jar.revision = revision
	return nil
}

type sessionResponseBody struct {
	io.ReadCloser
	finish func()
	once   sync.Once
}

func (body *sessionResponseBody) Close() error {
	err := body.ReadCloser.Close()
	body.once.Do(body.finish)
	return err
}

type bufferedResponseBody struct {
	io.Reader
	io.Closer
}

func responseAuthExpired(request *http.Request, response *http.Response) (bool, error) {
	if isAuthStatus(response.StatusCode) {
		return true, nil
	}
	if response.StatusCode != http.StatusOK || response.Body == nil {
		return false, nil
	}
	previous := response.Body
	payload, err := io.ReadAll(io.LimitReader(previous, maxErrorBodyBytes+1))
	response.Body = &bufferedResponseBody{Reader: io.MultiReader(bytes.NewReader(payload), previous), Closer: previous}
	if err != nil {
		if request.Context().Err() != nil {
			return false, request.Context().Err()
		}
		return false, fmt.Errorf("%w: incomplete data response", ErrUnavailable)
	}
	if len(payload) > maxErrorBodyBytes {
		return false, nil
	}
	var envelope struct {
		Status  json.RawMessage `json:"status_code"`
		Message string          `json:"status_msg"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return false, nil
	}
	status, err := parseStatus(envelope.Status)
	if err != nil {
		return false, nil
	}
	if request.URL.Hostname() == dataqHost {
		return status == http.StatusUnauthorized, nil
	}
	return request.URL.Hostname() == dataHost && status == -2 &&
		(request.URL.Path == chartPath || request.URL.Path == dataPermPath && strings.TrimSpace(envelope.Message) == "需要授权"), nil
}

func (manager *Manager) expiry() *time.Time {
	manager.sessionMutex.RLock()
	cookies := snapshotJarCookies(manager.jar.jar)
	manager.sessionMutex.RUnlock()
	var earliest time.Time
	for _, cookie := range cookies {
		if cookie == nil || cookie.Expires.IsZero() || !cookie.Expires.After(time.Now()) {
			continue
		}
		if earliest.IsZero() || cookie.Expires.Before(earliest) {
			earliest = cookie.Expires.UTC()
		}
	}
	if earliest.IsZero() {
		return nil
	}
	return &earliest
}

func (manager *Manager) recordFailure(err error) {
	code := classifyError(err)
	now := time.Now().UTC()
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	manager.lastErrorCode = code
	manager.lastFailure = nil
	var protocolError *LoginError
	if errors.As(err, &protocolError) {
		manager.lastFailure = cloneLoginFailure(&protocolError.Failure)
	}
	switch {
	case errors.Is(err, ErrCredentials), errors.Is(err, ErrChallenge):
		manager.state = StateActionNeeded
		manager.retryAt = nil
		manager.stopped = true
	case errors.Is(err, ErrAuthExpired):
		manager.state = StateExpired
		retry := now.Add(manager.options.Cooldown).UTC()
		manager.retryAt = &retry
	case errors.Is(err, ErrUnconfigured):
		manager.state = StateUnconfigured
		manager.retryAt = nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		manager.state = StateExpired
		manager.retryAt = nil
	default:
		retry := now.Add(manager.options.Cooldown).UTC()
		manager.state = StateCooldown
		manager.retryAt = &retry
	}
}

func (manager *Manager) recordSuccess() {
	now := time.Now().UTC()
	manager.stateMutex.Lock()
	manager.state = StateReady
	manager.lastErrorCode = ""
	manager.lastFailure = nil
	manager.retryAt = nil
	manager.lastRefreshAt = &now
	manager.refreshCount++
	manager.stopped = false
	manager.stateMutex.Unlock()
}

func (manager *Manager) isClosed() bool {
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	return manager.closed
}

func classifyError(err error) string {
	var protocolError *LoginError
	if errors.As(err, &protocolError) && errors.Is(err, ErrChallenge) && protocolError.Failure.Kind != "slider_required" {
		return "challenge"
	}
	switch {
	case errors.Is(err, ErrCredentials):
		return "credentials"
	case errors.Is(err, ErrChallenge):
		return "captcha"
	case errors.Is(err, ErrPermission):
		return "permission"
	case errors.Is(err, ErrAuthExpired):
		return "authentication"
	case errors.Is(err, ErrUnconfigured):
		return "unconfigured"
	case errors.Is(err, ErrCooldown):
		return "cooldown"
	default:
		return "unavailable"
	}
}

func cloneLoginFailure(failure *LoginFailure) *LoginFailure {
	if failure == nil {
		return nil
	}
	cloned := *failure
	if failure.BusinessCode != nil {
		code := *failure.BusinessCode
		cloned.BusinessCode = &code
	}
	return &cloned
}

func validateTarget(request *http.Request) error {
	if request.URL == nil || request.URL.Scheme != "https" || request.URL.User != nil || request.URL.Hostname() == "" {
		return fmt.Errorf("%w: target must use HTTPS", ErrPermission)
	}
	if request.URL.Port() != "" && request.URL.Port() != "443" {
		return fmt.Errorf("%w: target port is not allowed", ErrPermission)
	}
	host := strings.ToLower(request.URL.Hostname())
	path := request.URL.EscapedPath()
	switch host {
	case dataqHost:
		if request.Method == http.MethodPost && path == dataqPath {
			return nil
		}
	case dataHost:
		if request.Method == http.MethodGet && (path == chartPath || path == dataPermPath) {
			return nil
		}
	case ozoneHost:
		if request.Method == http.MethodGet && path == ozonePermPath {
			return nil
		}
	}
	return fmt.Errorf("%w: target is not allowed", ErrPermission)
}

func importOrigin(origin string) (*url.URL, error) {
	candidate := "https://" + strings.TrimSpace(origin)
	parsed, parseError := url.Parse(candidate)
	if parseError != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Port() != "" {
		return nil, fmt.Errorf("%w: import origin must be an approved HTTPS data domain", ErrCredentials)
	}
	host := strings.ToLower(parsed.Hostname())
	if host != dataqHost && host != dataHost && host != ozoneHost {
		return nil, fmt.Errorf("%w: import origin is not an approved data domain", ErrCredentials)
	}
	return parsed, nil
}

func rejectRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func hasCandidateCookies(jar http.CookieJar) bool {
	return hasUsableCookies(snapshotJarCookies(jar))
}

func hasUsableCookies(cookies []*http.Cookie) bool {
	now := time.Now()
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" {
			continue
		}
		if cookie.MaxAge < 0 {
			continue
		}
		if !cookie.Expires.IsZero() && !cookie.Expires.After(now) {
			continue
		}
		return true
	}
	return false
}

func snapshotJarCookies(jar http.CookieJar) []*http.Cookie {
	if tracked, ok := jar.(*trackedCookieJar); ok {
		cookies, _ := tracked.snapshot()
		return cookies
	}
	return nil
}

func isCookieDeletion(cookie *http.Cookie) bool {
	return cookie.MaxAge < 0 || cookie.MaxAge == 0 && !cookie.Expires.IsZero() && !cookie.Expires.After(time.Now())
}

func installCookies(jar http.CookieJar, cookies []*http.Cookie) error {
	for _, cookie := range cookies {
		if validateError := validateCookie(cookie); validateError != nil {
			return validateError
		}
		installed := cloneCookie(cookie)
		if !strings.HasPrefix(installed.Domain, ".") {
			installed.Domain = ""
		}
		if installed.MaxAge > 0 && !installed.Expires.IsZero() {
			installed.MaxAge = 0
		}
		jar.SetCookies(mustParseURL("https://"+strings.TrimPrefix(cookie.Domain, ".")+"/"), []*http.Cookie{installed})
	}
	return nil
}

func cloneCookie(cookie *http.Cookie) *http.Cookie {
	cloned := *cookie
	cloned.Unparsed = append([]string(nil), cookie.Unparsed...)
	return &cloned
}

func cloneRequest(request *http.Request) (*http.Request, error) {
	cloned := request.Clone(request.Context())
	if request.Body == nil || request.Body == http.NoBody {
		return cloned, nil
	}
	if request.GetBody == nil {
		return nil, fmt.Errorf("%w: request body is not replayable", ErrUnavailable)
	}
	body, bodyError := request.GetBody()
	if bodyError != nil {
		return nil, fmt.Errorf("%w: replay request body", ErrUnavailable)
	}
	cloned.Body = body
	return cloned, nil
}

func discardResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxErrorBodyBytes))
	_ = response.Body.Close()
}

func parseStatus(raw json.RawMessage) (int, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, errors.New("missing status")
	}
	var status int
	if unmarshalError := json.Unmarshal(trimmed, &status); unmarshalError == nil {
		return status, nil
	}
	var text string
	if unmarshalError := json.Unmarshal(trimmed, &text); unmarshalError != nil {
		return 0, errors.New("invalid status")
	}
	parsed, parseError := strconv.Atoi(strings.TrimSpace(text))
	if parseError != nil {
		return 0, errors.New("invalid status")
	}
	return parsed, nil
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func mustParseURL(value string) *url.URL {
	parsed, _ := url.Parse(value)
	return parsed
}

func filepathIsAbsolute(path string) bool {
	return len(path) > 0 && path[0] == '/'
}
