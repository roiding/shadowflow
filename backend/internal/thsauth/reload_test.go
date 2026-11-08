package thsauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const reloadDataURL = "https://dataq.10jqka.com.cn/fetch-data-server/fetch/v1/specific_data?date=2026-11-06"

func reloadCookies(value string) []*http.Cookie {
	return []*http.Cookie{{Name: "ticket", Value: value, Domain: ".10jqka.com.cn", Path: "/", Secure: true}}
}

func reloadResponse(request *http.Request) *http.Response {
	if cookieTestValue(request, "ticket") != "fixture-new-session" {
		return cookieTestResponse(request, http.StatusUnauthorized, `{"status_code":401}`)
	}
	return cookieTestResponse(request, http.StatusOK, `{"status_code":0,"data":{}}`)
}

func TestManagerReloadsExternalLoginAfterChallenge(testingT *testing.T) {
	for _, storage := range []string{"database-compatible", "private-file"} {
		testingT.Run(storage, func(testingT *testing.T) {
			var session SessionStore = &memorySessionStore{}
			if storage == "private-file" {
				directory := testingT.TempDir()
				if err := os.Chmod(directory, 0o700); err != nil {
					testingT.Fatal(err)
				}
				session = fileSessionStore{path: filepath.Join(directory, "session.json")}
			}
			var loginCalls atomic.Int32
			transport := cookieTestTransport(func(request *http.Request) (*http.Response, error) {
				return reloadResponse(request), nil
			})
			manager, err := New(Options{Session: session, AutoLogin: true, Transport: transport,
				Login: func(context.Context, *http.Client) error {
					loginCalls.Add(1)
					return ErrChallenge
				}})
			if err != nil {
				testingT.Fatal(err)
			}
			defer manager.Close()
			request := cookieTestRequest(testingT, http.MethodPost, reloadDataURL, `{"codes":["33:002578"]}`)
			if _, err := manager.Do(request); !errors.Is(err, ErrChallenge) {
				testingT.Fatalf("expected challenge, got %v", err)
			}
			if manager.Status().State != StateActionNeeded {
				testingT.Fatal("challenge did not stop automatic login")
			}
			manual, err := New(Options{Session: session, Transport: transport,
				Login: func(_ context.Context, client *http.Client) error {
					cookieTestSet(client, reloadCookies("fixture-new-session")[0])
					return nil
				}})
			if err != nil {
				testingT.Fatal(err)
			}
			defer manual.Close()
			if err := manual.Login(context.Background()); err != nil {
				testingT.Fatal(err)
			}
			response, err := manager.Do(cookieTestRequest(testingT, http.MethodPost, reloadDataURL, `{"codes":["33:002578"]}`))
			if err != nil {
				testingT.Fatal(err)
			}
			cookieTestReadResponse(testingT, response)
			status := manager.Status()
			if status.State != StateReady || status.LastErrorCode != "" || status.RetryAt != nil || loginCalls.Load() != 1 {
				testingT.Fatalf("manual login did not recover running manager: status=%+v calls=%d", status, loginCalls.Load())
			}
		})
	}
}

func TestManagerReloadDoesNotResetUnchangedChallenge(testingT *testing.T) {
	session := &memorySessionStore{}
	if err := session.SaveSession(context.Background(), reloadCookies("fixture-old-session")); err != nil {
		testingT.Fatal(err)
	}
	var loginCalls atomic.Int32
	manager, err := New(Options{Session: session, AutoLogin: true,
		Transport: cookieTestTransport(func(request *http.Request) (*http.Response, error) { return reloadResponse(request), nil }),
		Login: func(context.Context, *http.Client) error {
			loginCalls.Add(1)
			return ErrCredentials
		}})
	if err != nil {
		testingT.Fatal(err)
	}
	defer manager.Close()
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := manager.Do(cookieTestRequest(testingT, http.MethodPost, reloadDataURL, "{}")); !errors.Is(err, ErrCredentials) {
			testingT.Fatalf("unexpected stopped login error: %v", err)
		}
	}
	if loginCalls.Load() != 1 || manager.Status().State != StateActionNeeded {
		testingT.Fatal("unchanged persisted cookies reset the login stop")
	}
	expired := reloadCookies("fixture-expired-session")
	expired[0].Expires = time.Now().Add(-time.Hour)
	if err := session.SaveSession(context.Background(), expired); err != nil {
		testingT.Fatal(err)
	}
	if _, err := manager.Do(cookieTestRequest(testingT, http.MethodPost, reloadDataURL, "{}")); !errors.Is(err, ErrCredentials) {
		testingT.Fatalf("expired replacement resumed password login: %v", err)
	}
	if loginCalls.Load() != 1 || manager.Status().State != StateActionNeeded {
		testingT.Fatal("expired persisted cookies reset the login stop")
	}
}

func TestManagerReloadPreservesExternalSessionDuringStaleResponse(testingT *testing.T) {
	session := &memorySessionStore{}
	if err := session.SaveSession(context.Background(), reloadCookies("fixture-old-session")); err != nil {
		testingT.Fatal(err)
	}
	var requestBodies []string
	transport := cookieTestTransport(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		requestBodies = append(requestBodies, string(body))
		if cookieTestValue(request, "ticket") == "fixture-old-session" {
			if err := session.SaveSession(context.Background(), reloadCookies("fixture-new-session")); err != nil {
				return nil, err
			}
			response := reloadResponse(request)
			response.Header.Set("Set-Cookie", "ticket=fixture-stale-rotation; Domain=.10jqka.com.cn; Path=/; Secure")
			return response, nil
		}
		return reloadResponse(request), nil
	})
	manager, err := New(Options{Session: session, Transport: transport})
	if err != nil {
		testingT.Fatal(err)
	}
	defer manager.Close()
	const payload = `{"codes":["33:002578"],"date":"2026-11-06"}`
	response, err := manager.Do(cookieTestRequest(testingT, http.MethodPost, reloadDataURL, payload))
	if err != nil {
		testingT.Fatal(err)
	}
	cookieTestReadResponse(testingT, response)
	if len(requestBodies) != 2 || requestBodies[0] != payload || requestBodies[1] != payload {
		testingT.Fatal("external login did not replay the exact original POST once")
	}
	cookies, present, err := session.LoadSession(context.Background())
	if err != nil || !present || !cookieTestHasValue(cookies, "ticket", "fixture-new-session") {
		testingT.Fatalf("stale response overwrote the external login: %v", err)
	}
	if manager.Status().AutoLogin {
		testingT.Fatal("test requires password auto-login to remain disabled")
	}
}

func TestManagerReloadConcurrentStaleResponsesShareExternalSession(testingT *testing.T) {
	session := &memorySessionStore{}
	if err := session.SaveSession(context.Background(), reloadCookies("fixture-old-session")); err != nil {
		testingT.Fatal(err)
	}
	const requests = 12
	started := make(chan struct{}, requests)
	release := make(chan struct{})
	var loginCalls atomic.Int32
	manager, err := New(Options{Session: session, AutoLogin: true,
		Transport: cookieTestTransport(func(request *http.Request) (*http.Response, error) {
			if cookieTestValue(request, "ticket") == "fixture-old-session" {
				started <- struct{}{}
				<-release
				response := reloadResponse(request)
				response.Header.Set("Set-Cookie", "ticket=fixture-stale; Domain=.10jqka.com.cn; Path=/; Secure")
				return response, nil
			}
			return reloadResponse(request), nil
		}),
		Login: func(context.Context, *http.Client) error {
			loginCalls.Add(1)
			return ErrCredentials
		}})
	if err != nil {
		testingT.Fatal(err)
	}
	defer manager.Close()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	results := make(chan error, requests)
	for index := 0; index < requests; index++ {
		request := cookieTestRequest(testingT, http.MethodPost, reloadDataURL, "{}")
		go func() {
			response, err := manager.Do(request)
			if response != nil {
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
			}
			results <- err
		}()
	}
	for index := 0; index < requests; index++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			testingT.Fatal("requests did not reach the original session")
		}
	}
	if err := session.SaveSession(context.Background(), reloadCookies("fixture-new-session")); err != nil {
		testingT.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	for index := 0; index < requests; index++ {
		select {
		case err := <-results:
			if err != nil {
				testingT.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			testingT.Fatal("request did not finish after the external login")
		}
	}
	if loginCalls.Load() != 0 || manager.Status().State != StateReady {
		testingT.Fatal("concurrent requests did not reuse the external session")
	}
	cookies, _, err := session.LoadSession(context.Background())
	if err != nil || !cookieTestHasValue(cookies, "ticket", "fixture-new-session") {
		testingT.Fatal("concurrent response corrupted the external session")
	}
}

func TestManagerReloadRecoversInitialStoreFailure(testingT *testing.T) {
	session := &memorySessionStore{loadErr: errors.New("fixture store unavailable")}
	manager, err := New(Options{Session: session,
		Transport: cookieTestTransport(func(request *http.Request) (*http.Response, error) { return reloadResponse(request), nil })})
	if err != nil {
		testingT.Fatal(err)
	}
	defer manager.Close()
	if manager.Status().State != StateActionNeeded {
		testingT.Fatal("initial storage error was not visible")
	}
	session.mutex.Lock()
	session.loadErr = nil
	session.mutex.Unlock()
	if err := session.SaveSession(context.Background(), reloadCookies("fixture-new-session")); err != nil {
		testingT.Fatal(err)
	}
	response, err := manager.Do(cookieTestRequest(testingT, http.MethodPost, reloadDataURL, "{}"))
	if err != nil {
		testingT.Fatal(err)
	}
	cookieTestReadResponse(testingT, response)
	if manager.Status().State != StateReady {
		testingT.Fatal("fixed shared session store did not recover without restart")
	}
}

type cancelableReloadStore struct {
	memorySessionStore
	blocked atomic.Bool
	started chan struct{}
}

func (store *cancelableReloadStore) LoadSession(ctx context.Context) ([]*http.Cookie, bool, error) {
	if store.blocked.Load() {
		close(store.started)
		<-ctx.Done()
		return nil, false, ctx.Err()
	}
	return store.memorySessionStore.LoadSession(ctx)
}

func TestManagerCloseCancelsSessionReload(testingT *testing.T) {
	session := &cancelableReloadStore{started: make(chan struct{})}
	manager, err := New(Options{Session: session, RequestTimeout: time.Minute,
		Transport: cookieTestTransport(func(request *http.Request) (*http.Response, error) { return reloadResponse(request), nil })})
	if err != nil {
		testingT.Fatal(err)
	}
	defer manager.Close()
	session.blocked.Store(true)
	request := cookieTestRequest(testingT, http.MethodPost, reloadDataURL, "{}")
	finished := make(chan error, 1)
	go func() {
		_, err := manager.Do(request)
		finished <- err
	}()
	select {
	case <-session.started:
	case <-time.After(3 * time.Second):
		testingT.Fatal("session reload did not start")
	}
	if err := manager.Close(); err != nil {
		testingT.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			testingT.Fatalf("unexpected close error: %v", err)
		}
	case <-time.After(3 * time.Second):
		testingT.Fatal("close did not cancel the session store read")
	}
}
