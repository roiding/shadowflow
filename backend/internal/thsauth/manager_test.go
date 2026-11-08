package thsauth_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/thsauth"
)

const (
	dataqTargetURL       = "https://dataq.10jqka.com.cn/fetch-data-server/fetch/v1/specific_data"
	chartTargetURL       = "https://data.10jqka.com.cn/mobileapi/hotspot_focus/chart_config/v1/query_config"
	permissionTargetURL  = "https://data.10jqka.com.cn/mobileapi/hotspot_focus/permission/v1/query"
	dataqTargetPath      = "/fetch-data-server/fetch/v1/specific_data"
	chartTargetPath      = "/mobileapi/hotspot_focus/chart_config/v1/query_config"
	permissionTargetPath = "/mobileapi/hotspot_focus/permission/v1/query"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type loginFixture struct {
	calls  atomic.Int32
	cookie *http.Cookie
	action func(context.Context, *http.Client) error
}

func (fixture *loginFixture) login(ctx context.Context, client *http.Client) error {
	fixture.calls.Add(1)
	if fixture.action != nil {
		return fixture.action(ctx, client)
	}
	if fixture.cookie != nil {
		setFixtureCookie(client, fixture.cookie)
	}
	return nil
}

type fixtureRequestRecord struct {
	method        string
	target        string
	header        http.Header
	body          string
	contentLength int64
}

type fixtureCallResult struct {
	status int
	body   string
	err    error
}

func newFixtureTicket(value string) *http.Cookie {
	return &http.Cookie{
		Name:   "ticket",
		Value:  value,
		Domain: ".10jqka.com.cn",
		Path:   "/",
		Secure: true,
	}
}

func setFixtureCookie(client *http.Client, cookie *http.Cookie) {
	cloned := *cookie
	host := strings.TrimPrefix(cloned.Domain, ".")
	client.Jar.SetCookies(&url.URL{Scheme: "https", Host: host, Path: "/"}, []*http.Cookie{&cloned})
}

func fixtureCookieValue(request *http.Request, name string) string {
	cookie, cookieError := request.Cookie(name)
	if cookieError != nil {
		return ""
	}
	return cookie.Value
}

func fixtureHTTPResponse(request *http.Request, status int, body string) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       request,
	}
}

func captureFixtureRequest(request *http.Request) (fixtureRequestRecord, error) {
	var body []byte
	if request.Body != nil && request.Body != http.NoBody {
		readBody, readError := io.ReadAll(request.Body)
		if readError != nil {
			return fixtureRequestRecord{}, readError
		}
		body = readBody
		if closeError := request.Body.Close(); closeError != nil {
			return fixtureRequestRecord{}, closeError
		}
	}
	return fixtureRequestRecord{
		method:        request.Method,
		target:        request.URL.String(),
		header:        request.Header.Clone(),
		body:          string(body),
		contentLength: request.ContentLength,
	}, nil
}

func fixtureHeadersWithoutCookies(header http.Header) http.Header {
	cloned := header.Clone()
	cloned.Del("Cookie")
	return cloned
}

func newFixtureManager(testingT *testing.T, options thsauth.Options) *thsauth.Manager {
	testingT.Helper()
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 2 * time.Second
	}
	if options.RefreshTimeout <= 0 {
		options.RefreshTimeout = 8 * time.Second
	}
	if options.Cooldown <= 0 {
		options.Cooldown = time.Minute
	}
	manager, createError := thsauth.New(options)
	if createError != nil {
		testingT.Fatalf("create manager: %v", createError)
	}
	testingT.Cleanup(func() {
		_ = manager.Close()
	})
	return manager
}

func newFixturePost(testingT *testing.T, ctx context.Context) *http.Request {
	testingT.Helper()
	return newFixturePostBody(testingT, ctx, "symbol=600000&scope=fixture")
}

func newFixturePostBody(testingT *testing.T, ctx context.Context, body string) *http.Request {
	testingT.Helper()
	request, requestError := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		dataqTargetURL+"?fixture=alpha",
		strings.NewReader(body),
	)
	if requestError != nil {
		testingT.Fatal(requestError)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Fixture-Case", "exact-replay")
	return request
}

func newFixtureGet(testingT *testing.T, ctx context.Context, target string) *http.Request {
	testingT.Helper()
	request, requestError := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if requestError != nil {
		testingT.Fatal(requestError)
	}
	request.Header.Set("Accept", "application/json")
	return request
}

func readFixtureResponse(testingT *testing.T, response *http.Response) string {
	testingT.Helper()
	if response == nil || response.Body == nil {
		testingT.Fatal("response body is missing")
	}
	body, readError := io.ReadAll(response.Body)
	if readError != nil {
		testingT.Fatal(readError)
	}
	if closeError := response.Body.Close(); closeError != nil {
		testingT.Fatal(closeError)
	}
	return string(body)
}

func callFixture(manager *thsauth.Manager, request *http.Request) fixtureCallResult {
	response, responseError := manager.Do(request)
	if responseError != nil {
		return fixtureCallResult{err: responseError}
	}
	if response == nil || response.Body == nil {
		return fixtureCallResult{err: errors.New("fixture response is missing")}
	}
	body, readError := io.ReadAll(response.Body)
	closeError := response.Body.Close()
	if readError != nil {
		return fixtureCallResult{status: response.StatusCode, err: readError}
	}
	if closeError != nil {
		return fixtureCallResult{status: response.StatusCode, err: closeError}
	}
	return fixtureCallResult{status: response.StatusCode, body: string(body)}
}

func waitFixtureSignal(testingT *testing.T, signal <-chan struct{}, description string) {
	testingT.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		testingT.Fatalf("timed out waiting for %s", description)
	}
}

func TestDoRefreshesAfter401AndReplaysPOSTExactly(testingT *testing.T) {
	login := &loginFixture{cookie: newFixtureTicket("fresh-ticket")}
	var initialRecord fixtureRequestRecord
	var replayRecord fixtureRequestRecord
	initialCalls := 0
	replayCalls := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case dataqTargetPath:
			record, captureError := captureFixtureRequest(request)
			if captureError != nil {
				return nil, captureError
			}
			if fixtureCookieValue(request, "ticket") == "" {
				initialCalls++
				initialRecord = record
				return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
			}
			replayCalls++
			replayRecord = record
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0,"data":"replayed"}`), nil
		case chartTargetPath:
			if fixtureCookieValue(request, "ticket") != "fresh-ticket" {
				return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
			}
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0,"data":{}}`), nil
		default:
			return nil, fmt.Errorf("unexpected fixture request to %s", request.URL)
		}
	})
	manager := newFixtureManager(testingT, thsauth.Options{
		AutoLogin: true,
		Login:     login.login,
		Transport: transport,
	})
	request := newFixturePost(testingT, context.Background())
	response, responseError := manager.Do(request)
	if responseError != nil {
		testingT.Fatalf("do request: %v", responseError)
	}
	if response.StatusCode != http.StatusOK {
		testingT.Fatalf("unexpected replay status %d", response.StatusCode)
	}
	if body := readFixtureResponse(testingT, response); !strings.Contains(body, "replayed") {
		testingT.Fatalf("unexpected replay body %q", body)
	}
	if login.calls.Load() != 1 {
		testingT.Fatalf("expected one login, got %d", login.calls.Load())
	}
	if initialCalls != 1 || replayCalls != 1 {
		testingT.Fatalf("expected one initial and one replay call, got %d and %d", initialCalls, replayCalls)
	}
	if initialRecord.method != replayRecord.method ||
		initialRecord.target != replayRecord.target ||
		initialRecord.body != replayRecord.body ||
		initialRecord.contentLength != replayRecord.contentLength ||
		!reflect.DeepEqual(fixtureHeadersWithoutCookies(initialRecord.header), fixtureHeadersWithoutCookies(replayRecord.header)) {
		testingT.Fatalf("POST was not replayed exactly: initial=%+v replay=%+v", initialRecord, replayRecord)
	}
	status := manager.Status()
	if status.State != thsauth.StateReady || status.RefreshCount != 1 {
		testingT.Fatalf("unexpected status after replay: %+v", status)
	}
}

func TestSecond401AfterRefreshReturnsExpiredWithoutAnotherLogin(testingT *testing.T) {
	login := &loginFixture{cookie: newFixtureTicket("second-fresh")}
	dataCalls := atomic.Int32{}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case dataqTargetPath:
			if _, captureError := captureFixtureRequest(request); captureError != nil {
				return nil, captureError
			}
			dataCalls.Add(1)
			return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"still expired"}`), nil
		case chartTargetPath:
			if fixtureCookieValue(request, "ticket") != "second-fresh" {
				return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
			}
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0,"data":{}}`), nil
		default:
			return nil, fmt.Errorf("unexpected fixture request to %s", request.URL)
		}
	})
	manager := newFixtureManager(testingT, thsauth.Options{
		AutoLogin: true,
		Login:     login.login,
		Transport: transport,
	})
	response, responseError := manager.Do(newFixturePost(testingT, context.Background()))
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(responseError, thsauth.ErrAuthExpired) {
		testingT.Fatalf("expected auth expired after second 401, got %v", responseError)
	}
	if login.calls.Load() != 1 {
		testingT.Fatalf("second 401 started another login: %d calls", login.calls.Load())
	}
	if dataCalls.Load() != 2 {
		testingT.Fatalf("expected initial request and one replay, got %d data calls", dataCalls.Load())
	}
	status := manager.Status()
	if status.State != thsauth.StateExpired || status.RefreshCount != 1 {
		testingT.Fatalf("unexpected status after second 401: %+v", status)
	}
}

func TestTruncatedSuccessfulResponseFailsWithoutLogin(testingT *testing.T) {
	login := &loginFixture{cookie: newFixtureTicket("unused-ticket")}
	dataCalls := atomic.Int32{}
	payload := `{"status_code":0,"data":{"chart_list":[]}`
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != chartTargetPath {
			return nil, fmt.Errorf("unexpected truncated fixture request to %s", request.URL)
		}
		dataCalls.Add(1)
		response := fixtureHTTPResponse(request, http.StatusOK, payload)
		response.ContentLength = int64(len(payload) + 64)
		response.Body = io.NopCloser(io.MultiReader(strings.NewReader(payload), &fixtureErrorReader{err: io.ErrUnexpectedEOF}))
		return response, nil
	})
	manager := newFixtureManager(testingT, thsauth.Options{
		AutoLogin: true,
		Login:     login.login,
		Transport: transport,
	})
	response, responseError := manager.Do(newFixtureGet(testingT, context.Background(), chartTargetURL))
	if response != nil {
		_ = response.Body.Close()
	}
	if responseError == nil {
		testingT.Fatal("truncated response with complete JSON prefix was accepted")
	}
	if !errors.Is(responseError, thsauth.ErrUnavailable) {
		testingT.Fatalf("truncated response returned an unexpected error: %v", responseError)
	}
	if login.calls.Load() != 0 {
		testingT.Fatalf("truncated response started %d logins", login.calls.Load())
	}
	if dataCalls.Load() != 1 {
		testingT.Fatalf("truncated response unexpectedly retried: %d data calls", dataCalls.Load())
	}
}

type fixtureErrorReader struct {
	err error
}

func (reader *fixtureErrorReader) Read([]byte) (int, error) {
	return 0, reader.err
}

func TestFixedTargetRejectsCrossOriginRedirectWithoutCookies(testingT *testing.T) {
	login := &loginFixture{cookie: newFixtureTicket("redirect-ticket")}
	var targetCookie string
	var targetAuthorization string
	var redirectCookie string
	var redirectAuthorization string
	targetServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetCookie = request.Header.Get("Cookie")
		targetAuthorization = request.Header.Get("Authorization")
		writer.Header().Set("Location", "https://redirect.fixture.example/collect")
		writer.WriteHeader(http.StatusFound)
	}))
	defer targetServer.Close()
	redirectServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirectCookie = request.Header.Get("Cookie")
		redirectAuthorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status_code":0}`))
	}))
	defer redirectServer.Close()
	targetURL, parseError := url.Parse(targetServer.URL)
	if parseError != nil {
		testingT.Fatal(parseError)
	}
	redirectURL, parseError := url.Parse(redirectServer.URL)
	if parseError != nil {
		testingT.Fatal(parseError)
	}
	dataqHostName := mustFixtureURL(testingT, dataqTargetURL).Hostname()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Hostname() {
		case dataqHostName:
			request = request.Clone(request.Context())
			request.Header.Set("Cookie", "ticket=redirect-ticket")
			request.Header.Set("Authorization", "Bearer fixture-secret")
			request.URL.Scheme = targetURL.Scheme
			request.URL.Host = targetURL.Host
			return targetServer.Client().Transport.RoundTrip(request)
		case "redirect.fixture.example":
			request = request.Clone(request.Context())
			request.Header.Set("Authorization", "Bearer fixture-secret")
			request.URL.Scheme = redirectURL.Scheme
			request.URL.Host = redirectURL.Host
			return redirectServer.Client().Transport.RoundTrip(request)
		default:
			return nil, fmt.Errorf("unexpected redirect fixture host %s", request.URL.Hostname())
		}
	})
	manager := newFixtureManager(testingT, thsauth.Options{
		AutoLogin: true,
		Login:     login.login,
		Transport: transport,
	})
	response, responseError := manager.Do(newFixturePostBody(testingT, context.Background(), "symbol=600000"))
	if responseError != nil {
		testingT.Fatalf("redirect fixture request failed: %v", responseError)
	}
	if response.StatusCode != http.StatusFound {
		testingT.Fatalf("cross-origin redirect was followed: status=%d", response.StatusCode)
	}
	if location := response.Header.Get("Location"); location != "https://redirect.fixture.example/collect" {
		testingT.Fatalf("original redirect response was replaced: %q", location)
	}
	_ = readFixtureResponse(testingT, response)
	if targetCookie != "ticket=redirect-ticket" || targetAuthorization != "Bearer fixture-secret" {
		testingT.Fatalf("fixed target did not receive its scoped request: cookie=%q authorization=%q",
			targetCookie, targetAuthorization)
	}
	if redirectCookie != "" {
		testingT.Fatalf("cookie leaked across origin redirect: %q", redirectCookie)
	}
	if redirectAuthorization != "" {
		testingT.Fatalf("authorization leaked across origin redirect: %q", redirectAuthorization)
	}
	if login.calls.Load() != 0 {
		testingT.Fatalf("redirect response unexpectedly started login: %d calls", login.calls.Load())
	}
}

func mustFixtureURL(testingT *testing.T, target string) *url.URL {
	testingT.Helper()
	parsed, parseError := url.Parse(target)
	if parseError != nil {
		testingT.Fatal(parseError)
	}
	return parsed
}

func TestDoRefreshesThroughLocalHTTPAndReplaysPOST(testingT *testing.T) {
	var serverMutex sync.Mutex
	var initialBody string
	var replayBody string
	logins := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case dataqTargetPath:
			body, readError := io.ReadAll(request.Body)
			if readError != nil {
				http.Error(writer, "invalid request body", http.StatusBadRequest)
				return
			}
			cookie, cookieError := request.Cookie("ticket")
			if cookieError != nil || cookie.Value != "local-fresh" {
				serverMutex.Lock()
				initialBody = string(body)
				serverMutex.Unlock()
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(`{"error":"expired"}`))
				return
			}
			serverMutex.Lock()
			replayBody = string(body)
			serverMutex.Unlock()
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"status_code":0,"data":"local-replay"}`))
		case chartTargetPath:
			cookie, cookieError := request.Cookie("ticket")
			if cookieError != nil || cookie.Value != "local-fresh" {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"status_code":0,"data":{}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	serverURL, parseError := url.Parse(server.URL)
	if parseError != nil {
		testingT.Fatal(parseError)
	}
	targetURL := dataqTargetURL + "?fixture=local"
	login := func(ctx context.Context, client *http.Client) error {
		serverMutex.Lock()
		logins++
		serverMutex.Unlock()
		cookie := &http.Cookie{
			Name:     "ticket",
			Value:    "local-fresh",
			Domain:   ".10jqka.com.cn",
			Path:     "/",
			Secure:   true,
			HttpOnly: true,
		}
		setFixtureCookie(client, cookie)
		return nil
	}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Hostname() != serverURL.Hostname() {
			request = request.Clone(request.Context())
			request.URL.Scheme = serverURL.Scheme
			request.URL.Host = serverURL.Host
		}
		return server.Client().Transport.RoundTrip(request)
	})
	manager := newFixtureManager(testingT, thsauth.Options{
		AutoLogin: true,
		Login:     login,
		Transport: transport,
	})
	request, requestError := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		targetURL,
		strings.NewReader("symbol=600000&scope=local"),
	)
	if requestError != nil {
		testingT.Fatal(requestError)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Fixture-Local", "exact-replay")
	response, responseError := manager.Do(request)
	if responseError != nil {
		testingT.Fatalf("local HTTP request failed: %v", responseError)
	}
	if body := readFixtureResponse(testingT, response); !strings.Contains(body, "local-replay") {
		testingT.Fatalf("unexpected local replay body %q", body)
	}
	serverMutex.Lock()
	defer serverMutex.Unlock()
	if logins != 1 {
		testingT.Fatalf("expected one local login, got %d", logins)
	}
	if initialBody != "symbol=600000&scope=local" || replayBody != initialBody {
		testingT.Fatalf("local POST body was not replayed exactly: initial=%q replay=%q", initialBody, replayBody)
	}
}

func TestConcurrentExpiredRequestsShareOneRefresh(testingT *testing.T) {
	const requestCount = 8
	login := &loginFixture{action: func(ctx context.Context, client *http.Client) error {
		setFixtureCookie(client, newFixtureTicket("shared-fresh"))
		return nil
	}}
	loginGate := make(chan struct{})
	loginEntered := make(chan struct{})
	var loginOnce sync.Once
	login.action = func(ctx context.Context, client *http.Client) error {
		loginOnce.Do(func() {
			close(loginEntered)
		})
		select {
		case <-loginGate:
		case <-ctx.Done():
			return ctx.Err()
		}
		setFixtureCookie(client, newFixtureTicket("shared-fresh"))
		return nil
	}
	initialArrivals := atomic.Int32{}
	allInitialArrivals := make(chan struct{})
	var arrivalOnce sync.Once
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case dataqTargetPath:
			if _, captureError := captureFixtureRequest(request); captureError != nil {
				return nil, captureError
			}
			if fixtureCookieValue(request, "ticket") != "shared-fresh" {
				if initialArrivals.Add(1) == requestCount {
					arrivalOnce.Do(func() {
						close(allInitialArrivals)
					})
				}
				select {
				case <-allInitialArrivals:
				case <-request.Context().Done():
					return nil, request.Context().Err()
				}
				return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
			}
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0}`), nil
		case chartTargetPath:
			if fixtureCookieValue(request, "ticket") != "shared-fresh" {
				return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
			}
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0,"data":{}}`), nil
		default:
			return nil, fmt.Errorf("unexpected fixture request to %s", request.URL)
		}
	})
	manager := newFixtureManager(testingT, thsauth.Options{
		AutoLogin: true,
		Login:     login.login,
		Transport: transport,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := make(chan fixtureCallResult, requestCount)
	for index := 0; index < requestCount; index++ {
		go func(requestIndex int) {
			request, requestError := http.NewRequestWithContext(
				ctx,
				http.MethodPost,
				dataqTargetURL,
				strings.NewReader(fmt.Sprintf("request=%d", requestIndex)),
			)
			if requestError != nil {
				results <- fixtureCallResult{err: requestError}
				return
			}
			results <- callFixture(manager, request)
		}(index)
	}
	waitFixtureSignal(testingT, loginEntered, "shared refresh to start")
	close(loginGate)
	for index := 0; index < requestCount; index++ {
		result := <-results
		if result.err != nil {
			testingT.Fatalf("concurrent request failed: %v", result.err)
		}
		if result.status != http.StatusOK {
			testingT.Fatalf("unexpected concurrent response status %d", result.status)
		}
	}
	if login.calls.Load() != 1 {
		testingT.Fatalf("expected one shared login, got %d", login.calls.Load())
	}
	if initialArrivals.Load() != requestCount {
		testingT.Fatalf("expected %d initial requests, got %d", requestCount, initialArrivals.Load())
	}
	status := manager.Status()
	if status.State != thsauth.StateReady || status.RefreshCount != 1 {
		testingT.Fatalf("unexpected status after shared refresh: %+v", status)
	}
}

func TestLateStale401ReplaysWithoutSecondRefresh(testingT *testing.T) {
	login := &loginFixture{cookie: newFixtureTicket("late-fresh")}
	lateInitialArrived := make(chan struct{})
	releaseLateInitial := make(chan struct{})
	var arrivalOnce sync.Once
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseLateInitial)
		})
	}
	defer release()
	replayCount := atomic.Int32{}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case dataqTargetPath:
			record, captureError := captureFixtureRequest(request)
			if captureError != nil {
				return nil, captureError
			}
			if fixtureCookieValue(request, "ticket") == "" {
				if record.body == "case=late" {
					arrivalOnce.Do(func() {
						close(lateInitialArrived)
					})
					select {
					case <-releaseLateInitial:
					case <-request.Context().Done():
						return nil, request.Context().Err()
					}
				}
				return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
			}
			replayCount.Add(1)
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0}`), nil
		case chartTargetPath:
			if fixtureCookieValue(request, "ticket") != "late-fresh" {
				return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
			}
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0,"data":{}}`), nil
		default:
			return nil, fmt.Errorf("unexpected fixture request to %s", request.URL)
		}
	})
	manager := newFixtureManager(testingT, thsauth.Options{
		AutoLogin: true,
		Login:     login.login,
		Transport: transport,
	})
	lateContext, cancelLate := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelLate()
	lateResult := make(chan fixtureCallResult, 1)
	go func() {
		lateResult <- callFixture(manager, newFixturePostBody(testingT, lateContext, "case=late"))
	}()
	waitFixtureSignal(testingT, lateInitialArrived, "late stale request to arrive")
	firstResponse, firstError := manager.Do(newFixturePostBody(testingT, context.Background(), "case=current"))
	if firstError != nil {
		testingT.Fatalf("current request failed: %v", firstError)
	}
	if body := readFixtureResponse(testingT, firstResponse); !strings.Contains(body, `"status_code":0`) {
		testingT.Fatalf("current request did not refresh: %q", body)
	}
	if login.calls.Load() != 1 {
		testingT.Fatalf("expected initial refresh login, got %d", login.calls.Load())
	}
	release()
	select {
	case result := <-lateResult:
		if result.err != nil || result.status != http.StatusOK {
			testingT.Fatalf("late stale request was not replayed: %+v", result)
		}
	case <-time.After(5 * time.Second):
		testingT.Fatal("late stale request did not complete")
	}
	if login.calls.Load() != 1 {
		testingT.Fatalf("late stale 401 started another login: %d calls", login.calls.Load())
	}
	if replayCount.Load() != 2 {
		testingT.Fatalf("expected both requests to replay once, got %d replays", replayCount.Load())
	}
	status := manager.Status()
	if status.State != thsauth.StateReady || status.RefreshCount != 1 {
		testingT.Fatalf("unexpected status after late stale 401: %+v", status)
	}
}

func TestStatusAndConcurrentRequestsDoNotDeadlock(testingT *testing.T) {
	login := &loginFixture{action: func(ctx context.Context, client *http.Client) error {
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		setFixtureCookie(client, newFixtureTicket("deadlock-fresh"))
		return nil
	}}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case dataqTargetPath:
			record, captureError := captureFixtureRequest(request)
			if captureError != nil {
				return nil, captureError
			}
			if fixtureCookieValue(request, "ticket") == "" {
				if record.body == "symbol=600000" {
					select {
					case <-time.After(time.Millisecond):
					case <-request.Context().Done():
						return nil, request.Context().Err()
					}
				}
				return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
			}
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0}`), nil
		case chartTargetPath:
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0,"data":{}}`), nil
		default:
			return nil, fmt.Errorf("unexpected fixture request to %s", request.URL)
		}
	})
	manager, createError := thsauth.New(thsauth.Options{
		RequestTimeout: 2 * time.Second,
		Transport:      transport,
		AutoLogin:      true,
		Login:          login.login,
	})
	if createError != nil {
		testingT.Fatalf("create manager: %v", createError)
	}
	const workers = 16
	const iterations = 50
	start := make(chan struct{})
	completed := make(chan struct{}, workers*iterations)
	workerErrors := make(chan error, workers)
	requestContext, cancelRequest := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRequest()
	for worker := 0; worker < workers; worker++ {
		go func(workerIndex int) {
			<-start
			for iteration := 0; iteration < iterations; iteration++ {
				if workerIndex%2 == 0 {
					status := manager.Status()
					if status.State == "" {
						workerErrors <- errors.New("empty status state")
						return
					}
					completed <- struct{}{}
					continue
				}
				request, requestError := http.NewRequestWithContext(
					requestContext,
					http.MethodPost,
					dataqTargetURL,
					strings.NewReader("symbol=600000"),
				)
				if requestError != nil {
					workerErrors <- requestError
					return
				}
				if result := callFixture(manager, request); result.err != nil {
					workerErrors <- result.err
					return
				}
				completed <- struct{}{}
			}
		}(worker)
	}
	close(start)
	for index := 0; index < workers*iterations; index++ {
		select {
		case <-completed:
		case workerError := <-workerErrors:
			testingT.Fatalf("concurrent status/request worker failed: %v", workerError)
		case <-time.After(10 * time.Second):
			testingT.Fatal("Status and concurrent requests deadlocked")
		}
	}
	closed := make(chan error, 1)
	go func() {
		closed <- manager.Close()
	}()
	select {
	case closeError := <-closed:
		if closeError != nil {
			testingT.Fatalf("close failed: %v", closeError)
		}
	case <-time.After(5 * time.Second):
		testingT.Fatal("manager did not close")
	}
}

func TestNonAuthenticationFailuresDoNotLogin(testingT *testing.T) {
	scenarios := []struct {
		name           string
		status         int
		transportError error
	}{
		{name: "forbidden", status: http.StatusForbidden},
		{name: "permission payload", status: http.StatusForbidden},
		{name: "rate limited", status: http.StatusTooManyRequests},
		{name: "server failure", status: http.StatusBadGateway},
		{name: "network failure", transportError: errors.New("fixture network failure")},
	}
	for _, scenario := range scenarios {
		testingT.Run(scenario.name, func(testingT *testing.T) {
			login := &loginFixture{cookie: newFixtureTicket("unused-ticket")}
			attempts := atomic.Int32{}
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				attempts.Add(1)
				if scenario.transportError != nil {
					return nil, scenario.transportError
				}
				return fixtureHTTPResponse(request, scenario.status, `{"status_code":-2,"status_msg":"permission denied"}`), nil
			})
			manager := newFixtureManager(testingT, thsauth.Options{
				AutoLogin: true,
				Login:     login.login,
				Transport: transport,
			})
			result := callFixture(manager, newFixturePost(testingT, context.Background()))
			if scenario.transportError != nil {
				if result.err == nil || !errors.Is(result.err, scenario.transportError) {
					testingT.Fatalf("expected network failure, got %+v", result)
				}
			} else {
				if result.err != nil || result.status != scenario.status {
					testingT.Fatalf("expected response status %d, got %+v", scenario.status, result)
				}
			}
			if attempts.Load() != 1 {
				testingT.Fatalf("expected one upstream attempt, got %d", attempts.Load())
			}
			if login.calls.Load() != 0 {
				testingT.Fatalf("non-auth failure started %d logins", login.calls.Load())
			}
		})
	}
}

func TestPermissionPayloadDoesNotLogin(testingT *testing.T) {
	login := &loginFixture{cookie: newFixtureTicket("unused-ticket")}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != permissionTargetPath {
			return nil, fmt.Errorf("unexpected permission fixture request to %s", request.URL)
		}
		return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":-2,"status_msg":"permission denied"}`), nil
	})
	manager := newFixtureManager(testingT, thsauth.Options{
		AutoLogin: true,
		Login:     login.login,
		Transport: transport,
	})
	response, responseError := manager.Do(newFixtureGet(testingT, context.Background(), permissionTargetURL))
	if responseError != nil {
		testingT.Fatalf("permission request failed: %v", responseError)
	}
	if body := readFixtureResponse(testingT, response); !strings.Contains(body, `"status_code":-2`) {
		testingT.Fatalf("permission response changed unexpectedly: %q", body)
	}
	if login.calls.Load() != 0 {
		testingT.Fatalf("permission payload started %d logins", login.calls.Load())
	}
}

func TestAutoLoginDisabledDoesNotAttemptLogin(testingT *testing.T) {
	login := &loginFixture{cookie: newFixtureTicket("unused-ticket")}
	attempts := atomic.Int32{}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
	})
	manager := newFixtureManager(testingT, thsauth.Options{
		AutoLogin: false,
		Login:     login.login,
		Transport: transport,
	})
	response, responseError := manager.Do(newFixturePost(testingT, context.Background()))
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(responseError, thsauth.ErrAuthExpired) {
		testingT.Fatalf("expected auth expired, got %v", responseError)
	}
	if attempts.Load() != 1 || login.calls.Load() != 0 {
		testingT.Fatalf("auto login disabled still attempted login: requests=%d logins=%d", attempts.Load(), login.calls.Load())
	}
	if status := manager.Status(); status.State != thsauth.StateExpired {
		testingT.Fatalf("unexpected disabled-auto-login status: %+v", status)
	}
}

func TestPrivateChartAuthorizationPayloadRefreshesAndReplays(testingT *testing.T) {
	login := &loginFixture{cookie: newFixtureTicket("private-fresh")}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != chartTargetPath {
			return nil, fmt.Errorf("unexpected private fixture request to %s", request.URL)
		}
		if fixtureCookieValue(request, "ticket") != "private-fresh" {
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":-2,"status_msg":"需要授权"}`), nil
		}
		return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0,"data":{"chart_list":[]}}`), nil
	})
	manager := newFixtureManager(testingT, thsauth.Options{
		AutoLogin: true,
		Login:     login.login,
		Transport: transport,
	})
	request := newFixtureGet(testingT, context.Background(), chartTargetURL)
	response, responseError := manager.Do(request)
	if responseError != nil {
		testingT.Fatalf("do private chart request: %v", responseError)
	}
	if response.StatusCode != http.StatusOK {
		testingT.Fatalf("unexpected private chart replay status %d", response.StatusCode)
	}
	body := readFixtureResponse(testingT, response)
	if !strings.Contains(body, `"status_code":0`) {
		testingT.Fatalf("private chart authorization payload was not refreshed: %q", body)
	}
	if login.calls.Load() != 1 {
		testingT.Fatalf("expected one private chart login, got %d", login.calls.Load())
	}
}

func TestRefreshFailureCooldownAndExplicitLoginRecovery(testingT *testing.T) {
	var phase atomic.Int32
	login := &loginFixture{action: func(ctx context.Context, client *http.Client) error {
		if phase.Load() == 0 {
			return fmt.Errorf("%w: fixture outage", thsauth.ErrUnavailable)
		}
		setFixtureCookie(client, newFixtureTicket("cooldown-fresh"))
		return nil
	}}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case dataqTargetPath:
			if _, captureError := captureFixtureRequest(request); captureError != nil {
				return nil, captureError
			}
			if fixtureCookieValue(request, "ticket") != "cooldown-fresh" {
				return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
			}
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0}`), nil
		case chartTargetPath:
			if fixtureCookieValue(request, "ticket") != "cooldown-fresh" {
				return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
			}
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0,"data":{}}`), nil
		default:
			return nil, fmt.Errorf("unexpected fixture request to %s", request.URL)
		}
	})
	manager := newFixtureManager(testingT, thsauth.Options{
		AutoLogin: true,
		Login:     login.login,
		Transport: transport,
		Cooldown:  time.Minute,
	})
	firstResponse, firstError := manager.Do(newFixturePost(testingT, context.Background()))
	if firstResponse != nil {
		_ = firstResponse.Body.Close()
	}
	if !errors.Is(firstError, thsauth.ErrUnavailable) {
		testingT.Fatalf("expected unavailable refresh failure, got %v", firstError)
	}
	status := manager.Status()
	if status.State != thsauth.StateCooldown || status.RetryAt == nil || !status.RetryAt.After(time.Now()) {
		testingT.Fatalf("refresh failure did not enter cooldown: %+v", status)
	}
	secondResponse, secondError := manager.Do(newFixturePost(testingT, context.Background()))
	if secondResponse != nil {
		_ = secondResponse.Body.Close()
	}
	if !errors.Is(secondError, thsauth.ErrCooldown) {
		testingT.Fatalf("expected cooldown rejection, got %v", secondError)
	}
	if login.calls.Load() != 1 {
		testingT.Fatalf("cooldown retried login %d times", login.calls.Load())
	}
	phase.Store(1)
	if loginError := manager.Login(context.Background()); loginError != nil {
		testingT.Fatalf("explicit login did not clear cooldown: %v", loginError)
	}
	status = manager.Status()
	if status.State != thsauth.StateReady || status.RetryAt != nil {
		testingT.Fatalf("explicit login did not restore ready state: %+v", status)
	}
	result := callFixture(manager, newFixturePost(testingT, context.Background()))
	if result.err != nil || result.status != http.StatusOK {
		testingT.Fatalf("request after explicit login failed: %+v", result)
	}
	if login.calls.Load() != 2 {
		testingT.Fatalf("unexpected explicit login count %d", login.calls.Load())
	}
}

func TestCredentialAndChallengeFailuresStopAutomaticRetry(testingT *testing.T) {
	scenarios := []struct {
		name      string
		failure   error
		errorCode string
	}{
		{name: "wrong password", failure: thsauth.ErrCredentials, errorCode: "credentials"},
		{name: "captcha challenge", failure: thsauth.ErrChallenge, errorCode: "captcha"},
	}
	for _, scenario := range scenarios {
		testingT.Run(scenario.name, func(testingT *testing.T) {
			var phase atomic.Int32
			login := &loginFixture{action: func(ctx context.Context, client *http.Client) error {
				if phase.Load() == 0 {
					return fmt.Errorf("%w: fixture terminal failure", scenario.failure)
				}
				setFixtureCookie(client, newFixtureTicket("manual-fresh"))
				return nil
			}}
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case dataqTargetPath:
					if _, captureError := captureFixtureRequest(request); captureError != nil {
						return nil, captureError
					}
					if fixtureCookieValue(request, "ticket") != "manual-fresh" {
						return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
					}
					return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0}`), nil
				case chartTargetPath:
					if fixtureCookieValue(request, "ticket") != "manual-fresh" {
						return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
					}
					return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0,"data":{}}`), nil
				default:
					return nil, fmt.Errorf("unexpected fixture request to %s", request.URL)
				}
			})
			manager := newFixtureManager(testingT, thsauth.Options{
				AutoLogin: true,
				Login:     login.login,
				Transport: transport,
			})
			firstResponse, firstError := manager.Do(newFixturePost(testingT, context.Background()))
			if firstResponse != nil {
				_ = firstResponse.Body.Close()
			}
			if !errors.Is(firstError, scenario.failure) {
				testingT.Fatalf("expected terminal login error, got %v", firstError)
			}
			status := manager.Status()
			if status.State != thsauth.StateActionNeeded || status.LastErrorCode != scenario.errorCode {
				testingT.Fatalf("terminal login failure status mismatch: %+v", status)
			}
			secondResponse, secondError := manager.Do(newFixturePost(testingT, context.Background()))
			if secondResponse != nil {
				_ = secondResponse.Body.Close()
			}
			if secondError == nil {
				testingT.Fatal("automatic retry unexpectedly succeeded after terminal login failure")
			}
			if login.calls.Load() != 1 {
				testingT.Fatalf("terminal failure allowed automatic re-login: %d calls", login.calls.Load())
			}
			phase.Store(1)
			if loginError := manager.Login(context.Background()); loginError != nil {
				testingT.Fatalf("explicit login did not recover: %v", loginError)
			}
			status = manager.Status()
			if status.State != thsauth.StateReady {
				testingT.Fatalf("unexpected status after explicit recovery: %+v", status)
			}
			result := callFixture(manager, newFixturePost(testingT, context.Background()))
			if result.err != nil || result.status != http.StatusOK {
				testingT.Fatalf("request after explicit recovery failed: %+v", result)
			}
			if login.calls.Load() != 2 {
				testingT.Fatalf("unexpected explicit recovery login count %d", login.calls.Load())
			}
		})
	}
}

func TestCanceledWaiterDoesNotCancelSharedRefresh(testingT *testing.T) {
	loginGate := make(chan struct{})
	loginEntered := make(chan struct{})
	var loginOnce sync.Once
	login := &loginFixture{action: func(ctx context.Context, client *http.Client) error {
		loginOnce.Do(func() {
			close(loginEntered)
		})
		select {
		case <-loginGate:
		case <-ctx.Done():
			return ctx.Err()
		}
		setFixtureCookie(client, newFixtureTicket("survivor-fresh"))
		return nil
	}}
	staleArrivals := atomic.Int32{}
	twoStaleRequests := make(chan struct{})
	var staleOnce sync.Once
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case dataqTargetPath:
			if _, captureError := captureFixtureRequest(request); captureError != nil {
				return nil, captureError
			}
			if fixtureCookieValue(request, "ticket") != "survivor-fresh" {
				if staleArrivals.Add(1) == 2 {
					staleOnce.Do(func() {
						close(twoStaleRequests)
					})
				}
				return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
			}
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0}`), nil
		case chartTargetPath:
			if fixtureCookieValue(request, "ticket") != "survivor-fresh" {
				return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
			}
			return fixtureHTTPResponse(request, http.StatusOK, `{"status_code":0,"data":{}}`), nil
		default:
			return nil, fmt.Errorf("unexpected fixture request to %s", request.URL)
		}
	})
	manager := newFixtureManager(testingT, thsauth.Options{
		AutoLogin: true,
		Login:     login.login,
		Transport: transport,
	})
	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstResults := make(chan fixtureCallResult, 1)
	go func() {
		firstResults <- callFixture(manager, newFixturePost(testingT, firstContext))
	}()
	waitFixtureSignal(testingT, loginEntered, "shared refresh before cancellation")
	secondResults := make(chan fixtureCallResult, 1)
	go func() {
		secondResults <- callFixture(manager, newFixturePost(testingT, context.Background()))
	}()
	waitFixtureSignal(testingT, twoStaleRequests, "both callers to receive expired responses")
	cancelFirst()
	select {
	case firstResult := <-firstResults:
		if !errors.Is(firstResult.err, context.Canceled) {
			testingT.Fatalf("expected canceled caller error, got %+v", firstResult)
		}
	case <-time.After(5 * time.Second):
		testingT.Fatal("canceled caller did not return")
	}
	close(loginGate)
	select {
	case secondResult := <-secondResults:
		if secondResult.err != nil || secondResult.status != http.StatusOK {
			testingT.Fatalf("surviving caller was broken: %+v", secondResult)
		}
	case <-time.After(5 * time.Second):
		testingT.Fatal("surviving caller did not complete")
	}
	if login.calls.Load() != 1 {
		testingT.Fatalf("canceled caller started another login: %d calls", login.calls.Load())
	}
	status := manager.Status()
	if status.State != thsauth.StateReady || status.RefreshCount != 1 {
		testingT.Fatalf("unexpected status after shared cancellation: %+v", status)
	}
}

func TestCloseCancelsInFlightRefresh(testingT *testing.T) {
	refreshEntered := make(chan struct{})
	refreshCanceled := make(chan struct{})
	var enteredOnce sync.Once
	var canceledOnce sync.Once
	login := &loginFixture{action: func(ctx context.Context, client *http.Client) error {
		enteredOnce.Do(func() {
			close(refreshEntered)
		})
		<-ctx.Done()
		canceledOnce.Do(func() {
			close(refreshCanceled)
		})
		return ctx.Err()
	}}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != dataqTargetPath {
			return nil, fmt.Errorf("unexpected fixture request to %s", request.URL)
		}
		if _, captureError := captureFixtureRequest(request); captureError != nil {
			return nil, captureError
		}
		return fixtureHTTPResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
	})
	manager := newFixtureManager(testingT, thsauth.Options{
		AutoLogin:      true,
		Login:          login.login,
		Transport:      transport,
		RefreshTimeout: 30 * time.Second,
	})
	requestContext, cancelRequest := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRequest()
	results := make(chan fixtureCallResult, 1)
	go func() {
		results <- callFixture(manager, newFixturePost(testingT, requestContext))
	}()
	waitFixtureSignal(testingT, refreshEntered, "refresh to enter login")
	closeResults := make(chan error, 1)
	go func() {
		closeResults <- manager.Close()
	}()
	select {
	case closeError := <-closeResults:
		if closeError != nil {
			testingT.Fatalf("close failed: %v", closeError)
		}
	case <-time.After(5 * time.Second):
		testingT.Fatal("close did not cancel in-flight refresh")
	}
	select {
	case <-refreshCanceled:
	case <-time.After(5 * time.Second):
		testingT.Fatal("refresh context was not canceled by close")
	}
	select {
	case result := <-results:
		if result.err == nil {
			testingT.Fatalf("request unexpectedly succeeded after close: %+v", result)
		}
	case <-time.After(5 * time.Second):
		testingT.Fatal("request did not return after close canceled refresh")
	}
}
