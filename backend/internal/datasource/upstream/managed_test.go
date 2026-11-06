package upstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/proxypool"
)

type managedFixture struct {
	mu             sync.Mutex
	prewarms       int
	acquires       int
	feedback       int
	releases       int
	deletes        int
	ready          chan struct{}
	partial        bool
	empty          bool
	emptyOnAcquire int
	bindings       map[string]int
	connection     map[string]any
	counts         map[string]int
}

func newManagedFixture(test *testing.T, workers int) (*proxypool.Manager, *managedFixture) {
	test.Helper()
	fixture := &managedFixture{bindings: make(map[string]int)}
	server := httptest.NewServer(http.HandlerFunc(fixture.handle))
	test.Cleanup(server.Close)
	manager, err := proxypool.NewManager(proxypool.ManagerOptions{
		URL: server.URL, Token: "project-only-token", Workers: workers, MaxAttempts: 3,
		ControlTimeout: time.Second, AcquireTimeout: time.Second, PrewarmTimeout: time.Second, PollInterval: 5 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		test.Fatal(err)
	}
	test.Cleanup(manager.Stop)
	return manager, fixture
}

func (fixture *managedFixture) handle(writer http.ResponseWriter, request *http.Request) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	if request.Header.Get("Authorization") != "Bearer project-only-token" {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case request.URL.Path == "/api/v1/prewarm":
		var body struct {
			Count int `json:"count"`
		}
		if json.NewDecoder(request.Body).Decode(&body) != nil || body.Count < 1 {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		fixture.prewarms++
		jobID := fmt.Sprintf("warm-%d", fixture.prewarms)
		if fixture.counts == nil {
			fixture.counts = make(map[string]int)
		}
		fixture.counts[jobID] = body.Count
		writer.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(writer).Encode(map[string]any{"job_id": jobID, "kind": "prewarm", "state": "QUEUED", "target": body.Count, "ready": 0})
	case strings.HasSuffix(request.URL.Path, "/cancel"):
		_, _ = io.WriteString(writer, `{}`)
	case strings.HasPrefix(request.URL.Path, "/api/v1/jobs/"):
		jobID := strings.TrimPrefix(request.URL.Path, "/api/v1/jobs/")
		count := fixture.counts[jobID]
		state, ready := "COMPLETED", count
		if fixture.partial {
			state, ready = "PARTIAL", 0
		}
		if fixture.ready != nil {
			select {
			case <-fixture.ready:
			default:
				state, ready = "RUNNING", 4
			}
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"job_id": jobID, "kind": "prewarm",
			"state": state, "target": count, "ready": ready, "finished_at": time.Now().UTC()})
	case request.URL.Path == "/api/v1/proxies/acquire":
		fixture.acquires++
		if fixture.empty || fixture.acquires == fixture.emptyOnAcquire {
			_, _ = io.WriteString(writer, `{"requested":1,"delivered":0,"missing":1,"items":[],"reason":"NO_VALIDATED_RESOURCES"}`)
			return
		}
		var body struct {
			SessionID  string            `json:"session_id"`
			Validation map[string]string `json:"validation"`
		}
		if json.NewDecoder(request.Body).Decode(&body) != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		identity, exists := fixture.bindings[body.SessionID]
		if !exists {
			identity = fixture.acquires
			fixture.bindings[body.SessionID] = identity
		}
		now := time.Now().UTC()
		connection := fixture.connection
		if connection == nil {
			connection = map[string]any{"transport": "http", "host": fmt.Sprintf("proxy-%d.test", identity), "port": 8080}
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"requested": 1, "delivered": 1, "missing": 0, "session_id": body.SessionID,
			"session_changed": !exists, "binding_version": 1, "binding_expires_at": now.Add(10 * time.Minute),
			"items": []any{map[string]any{
				"lease_id": fmt.Sprintf("lease-%d", fixture.acquires), "source_id": "public-list",
				"connection":       connection,
				"lease_expires_at": now.Add(30 * time.Second), "provider_expires_at": nil,
				"validation": map[string]any{"mode": body.Validation["mode"], "target": body.Validation["target"], "completed_at": now, "valid_until": now.Add(30 * time.Second)},
			}},
		})
	case strings.HasSuffix(request.URL.Path, "/feedback"):
		fixture.feedback++
		_, _ = io.WriteString(writer, `{}`)
	case strings.HasSuffix(request.URL.Path, "/release"):
		fixture.releases++
		_, _ = io.WriteString(writer, `{}`)
	case strings.HasPrefix(request.URL.Path, "/api/v1/sessions/"):
		fixture.deletes++
		delete(fixture.bindings, strings.TrimPrefix(request.URL.Path, "/api/v1/sessions/"))
		_, _ = io.WriteString(writer, `{}`)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func managedResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

type brokenManagedBody struct{}

func (brokenManagedBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (brokenManagedBody) Close() error             { return nil }

func TestManagedTransportWaitsBeforeAnyBusinessRequest(testingT *testing.T) {
	manager, fixture := newManagedFixture(testingT, 4)
	fixture.ready = make(chan struct{})
	var businessCalls atomic.Int32
	transport := NewManagedTransport(bodyTestTransport(func(request *http.Request) (*http.Response, error) {
		businessCalls.Add(1)
		if request.Header.Get("Authorization") != "" {
			testingT.Error("control credential leaked to business request")
		}
		return managedResponse(http.StatusOK, `{"rc":0,"data":{}}`), nil
	}), manager, time.Second)
	finished := make(chan error, 1)
	go func() {
		response, err := (&http.Client{Transport: transport}).Get("https://push2his.eastmoney.com/api/qt/stock/kline/get?klt=101")
		if response != nil {
			_ = response.Body.Close()
		}
		finished <- err
	}()
	deadline := time.After(time.Second)
	for len(manager.Status().Prewarms) == 0 {
		select {
		case <-deadline:
			testingT.Fatal("request did not start prewarm")
		case <-time.After(time.Millisecond):
		}
	}
	if businessCalls.Load() != 0 || manager.Status().BusyWorkers != 0 {
		testingT.Fatal("prewarm did not gate business work")
	}
	close(fixture.ready)
	if err := <-finished; err != nil {
		testingT.Fatal(err)
	}
	if businessCalls.Load() != 1 {
		testingT.Fatal("business request did not run after prewarm completed")
	}
}

func TestManagedTransportSwitchesProxyAndRetriesIdenticalRequest(testingT *testing.T) {
	for _, failure := range []string{"empty", "truncated", "invalid-json", "http-403", "connection"} {
		testingT.Run(failure, func(test *testing.T) {
			manager, fixture := newManagedFixture(test, 1)
			var routes, targets []string
			transport := NewManagedTransport(bodyTestTransport(func(request *http.Request) (*http.Response, error) {
				routes = append(routes, request.URL.Host)
				targets = append(targets, request.Header.Get("X-Shadowflow-Target"))
				if len(routes) == 1 {
					switch failure {
					case "empty":
						return managedResponse(http.StatusOK, ""), nil
					case "truncated":
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: brokenManagedBody{}}, nil
					case "invalid-json":
						return managedResponse(http.StatusOK, "<html>blocked</html>"), nil
					case "http-403":
						return managedResponse(http.StatusForbidden, "denied"), nil
					case "connection":
						return nil, errors.New("connection failure containing private-proxy-password")
					}
				}
				return managedResponse(http.StatusOK, `{"rc":0,"data":{}}`), nil
			}), manager, time.Second)
			client := &http.Client{Transport: transport}
			target := "https://push2his.eastmoney.com/api/qt/stock/kline/get?secid=1.600519&klt=5&beg=20261105&end=20261105"
			response, err := client.Get(target)
			if err != nil {
				test.Fatal(err)
			}
			_ = response.Body.Close()
			response, err = client.Get(target)
			if err != nil {
				test.Fatal(err)
			}
			_ = response.Body.Close()
			if len(routes) != 3 || routes[0] == routes[1] || routes[1] != routes[2] {
				test.Fatalf("unexpected routes: %v", routes)
			}
			for _, requested := range targets {
				if requested != target {
					test.Fatalf("retry changed the logical request: %s", requested)
				}
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.feedback != 1 || fixture.deletes != 1 || fixture.acquires != 2 || fixture.releases != 1 {
				test.Fatalf("unexpected lifecycle feedback=%d delete=%d acquire=%d release=%d", fixture.feedback, fixture.deletes, fixture.acquires, fixture.releases)
			}
		})
	}
}

func TestManagedTransportHasFourGlobalWorkers(testingT *testing.T) {
	manager, _ := newManagedFixture(testingT, 4)
	started, release := make(chan struct{}, 5), make(chan struct{})
	var active, peak atomic.Int32
	transport := NewManagedTransport(bodyTestTransport(func(request *http.Request) (*http.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous && !peak.CompareAndSwap(previous, current); previous = peak.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
			return managedResponse(http.StatusOK, `{"rc":0,"data":{}}`), nil
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	}), manager, 2*time.Second)
	results := make(chan error, 5)
	for index := range 5 {
		go func() {
			response, err := (&http.Client{Transport: transport}).Get(fmt.Sprintf("https://push2his.eastmoney.com/api/qt/stock/kline/get?klt=%d", index+1))
			if response != nil {
				_ = response.Body.Close()
			}
			results <- err
		}()
	}
	for range 4 {
		select {
		case <-started:
		case <-time.After(time.Second):
			testingT.Fatal("four workers were not started")
		}
	}
	select {
	case <-started:
		testingT.Fatal("fifth simultaneous request escaped the limit")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for range 5 {
		if err := <-results; err != nil {
			testingT.Fatal(err)
		}
	}
	if peak.Load() != 4 {
		testingT.Fatalf("peak concurrency=%d", peak.Load())
	}
}

func TestManagedTransportPartialAndEmptyPoolNeverUseDirect(testingT *testing.T) {
	for _, state := range []string{"partial", "empty"} {
		testingT.Run(state, func(test *testing.T) {
			manager, fixture := newManagedFixture(test, 4)
			fixture.partial, fixture.empty = state == "partial", state == "empty"
			var calls atomic.Int32
			transport := NewManagedTransport(bodyTestTransport(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return managedResponse(http.StatusOK, `{}`), nil
			}), manager, time.Second)
			_, err := (&http.Client{Transport: transport}).Get("https://push2.eastmoney.com/api/qt/clist/get?fs=b:BK001")
			if err == nil || calls.Load() != 0 {
				test.Fatalf("unprepared/empty pool reached business transport: %v", err)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.prewarms > 2 || fixture.acquires > 3 {
				test.Fatal("unbounded empty-pool replenishment")
			}
		})
	}
}

func TestManagedTransportCancellationIsNotProxyFailure(testingT *testing.T) {
	manager, fixture := newManagedFixture(testingT, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := NewManagedTransport(bodyTestTransport(func(*http.Request) (*http.Response, error) {
		cancel()
		return nil, context.Canceled
	}), manager, time.Second)
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://push2his.eastmoney.com/api/qt/stock/kline/get", nil)
	if _, err := (&http.Client{Transport: transport}).Do(request); !errors.Is(err, context.Canceled) {
		testingT.Fatalf("cancellation was lost: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.feedback != 0 || fixture.acquires != 1 {
		testingT.Fatal("cancellation poisoned a proxy or retried")
	}
}

func TestManagedTransportValidNoDataDoesNotRotateProxy(testingT *testing.T) {
	manager, fixture := newManagedFixture(testingT, 1)
	transport := NewManagedTransport(bodyTestTransport(func(*http.Request) (*http.Response, error) {
		return managedResponse(http.StatusOK, `{"rc":0,"data":null}`), nil
	}), manager, time.Second)
	response, err := (&http.Client{Transport: transport}).Get("https://push2his.eastmoney.com/api/qt/stock/kline/get")
	if err != nil {
		testingT.Fatal(err)
	}
	_ = response.Body.Close()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.feedback != 0 || fixture.acquires != 1 {
		testingT.Fatal("valid business no-data was classified as proxy failure")
	}
}

func TestManagedTransportHonorsRetryAfterAcrossRequests(testingT *testing.T) {
	manager, _ := newManagedFixture(testingT, 1)
	var calls atomic.Int32
	transport := NewManagedTransport(bodyTestTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		response := managedResponse(http.StatusTooManyRequests, `{}`)
		response.Header.Set("Retry-After", "2")
		return response, nil
	}), manager, time.Second)
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://push2his.eastmoney.com/api/qt/stock/kline/get", nil)
		_, _ = (&http.Client{Transport: transport}).Do(request)
		cancel()
	}
	if calls.Load() != 1 {
		testingT.Fatalf("Retry-After was bypassed by a new request: %d", calls.Load())
	}
}

func TestManagedTransportUsesAuthenticatedCONNECTAndVerifiedTLS(testingT *testing.T) {
	var originCalls, connectCalls atomic.Int32
	origin := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		originCalls.Add(1)
		if request.Header.Get("Authorization") != "" || request.Header.Get("Proxy-Authorization") != "" {
			testingT.Error("control or proxy credential leaked to the origin")
		}
		_, _ = io.WriteString(writer, `{"rc":0,"data":{}}`)
	}))
	defer origin.Close()
	originURL, _ := url.Parse(origin.URL)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connectCalls.Add(1)
		if request.Method != http.MethodConnect || request.Host != originURL.Host ||
			request.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("proxy-user:")) {
			testingT.Error("incorrect authenticated proxy CONNECT")
			writer.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		target, err := net.DialTimeout("tcp", originURL.Host, time.Second)
		if err != nil {
			testingT.Error(err)
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		defer target.Close()
		client, buffered, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			testingT.Error(err)
			return
		}
		defer client.Close()
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		copied := make(chan struct{})
		go func() {
			_, _ = io.Copy(target, buffered)
			_ = target.Close()
			close(copied)
		}()
		_, _ = io.Copy(client, target)
		_ = client.Close()
		<-copied
	}))
	defer proxyServer.Close()
	manager, fixture := newManagedFixture(testingT, 1)
	proxyURL, _ := url.Parse(proxyServer.URL)
	proxyPort, _ := strconv.Atoi(proxyURL.Port())
	fixture.connection = map[string]any{"transport": "http", "host": proxyURL.Hostname(), "port": proxyPort, "username": "proxy-user", "password": ""}
	base := origin.Client().Transport.(*http.Transport)
	if base.TLSClientConfig.InsecureSkipVerify {
		testingT.Fatal("test unexpectedly disabled certificate verification")
	}
	transport := NewManagedTransport(base, manager, time.Second)
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport}).Get(origin.URL + "/api/qt/stock/kline/get?klt=5")
	if err != nil {
		testingT.Fatal(err)
	}
	_ = response.Body.Close()
	if originCalls.Load() != 1 || connectCalls.Load() != 1 {
		testingT.Fatalf("proxy was bypassed: origin=%d CONNECT=%d", originCalls.Load(), connectCalls.Load())
	}
}
