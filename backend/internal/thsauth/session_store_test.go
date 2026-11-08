package thsauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type memorySessionStore struct {
	mutex   sync.Mutex
	payload string
	saveErr error
	loadErr error
	saves   int
	loaded  bool
}

func (store *memorySessionStore) LoadSession(ctx context.Context) ([]*http.Cookie, bool, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.loaded = true
	if store.loadErr != nil {
		return nil, false, store.loadErr
	}
	if store.payload == "" {
		return nil, false, nil
	}
	cookies, err := DecodeSessionCookies(store.payload)
	if err != nil {
		return nil, false, err
	}
	return cookies, true, nil
}

func (store *memorySessionStore) SaveSession(ctx context.Context, cookies []*http.Cookie) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.saveErr != nil {
		return store.saveErr
	}
	store.saves++
	payload, err := EncodeSessionCookies(cookies)
	if err != nil {
		return err
	}
	store.payload = payload
	return nil
}

func TestManagerPersistsSessionThroughStore(testingT *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		testingT.Fatal(err)
	}
	keyDocument := fmt.Sprintf(`var thspubkey={"v":"fixture-key","m":"%X","p":"%X"};`, privateKey.N, privateKey.E)
	transport := passwordTransport(func(request *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: request}
		payload := ""
		switch request.URL.Path {
		case "/pubkey/default.js":
			payload = keyDocument
		case "/user/getGS":
			requestBody, _ := io.ReadAll(request.Body)
			requestValues, _ := url.ParseQuery(string(requestBody))
			salt := saltResponse{DSK: "fixture-dsk", DSV: "fixture-dsv"}
			salt.SSV = base64.StdEncoding.EncodeToString(xorBytes([]byte("gs=fixture-salt"), []byte(sha256Hex(requestValues.Get("crnd")+salt.DSK))))
			payload = fmt.Sprintf(`{"errorcode":0,"dsk":%q,"dsv":%q,"ssv":%q}`, salt.DSK, salt.DSV, salt.SSV)
		case "/login/dologinreturnjson2":
			payload = `{"errorcode":0}`
			response.Header.Set("Set-Cookie", "ticket=fixture-session; Domain=.10jqka.com.cn; Path=/; Secure; HttpOnly")
		case "/mobileapi/hotspot_focus/chart_config/v1/query_config":
			payload = `{"status_code":0,"data":{"chart_list":[]}}`
		default:
			testingT.Fatalf("unexpected login request path %s", request.URL.Path)
		}
		response.Body = io.NopCloser(strings.NewReader(payload))
		return response, nil
	})
	store := &memorySessionStore{}
	manager, err := New(Options{
		Session:   store,
		AutoLogin: false,
		Transport: transport,
		Login:     PasswordLoginWithSource(EnvCredentials("fixture-user", "fixture-secret")),
	})
	if err != nil {
		testingT.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Login(context.Background()); err != nil {
		testingT.Fatal(err)
	}
	if store.saves != 1 || store.payload == "" {
		testingT.Fatalf("session was not persisted through the store: saves=%d", store.saves)
	}
	if !strings.Contains(store.payload, "fixture-session") {
		testingT.Fatal("session payload lost the login cookies")
	}
	// A restarted manager loads the persisted session straight into ready state.
	restarted, err := New(Options{Session: store})
	if err != nil {
		testingT.Fatal(err)
	}
	defer restarted.Close()
	status := restarted.Status()
	if status.State != StateReady {
		testingT.Fatalf("restarted manager did not restore the session: %+v", status)
	}
}

func TestManagerKeepsWorkingWhenStoreFails(testingT *testing.T) {
	store := &memorySessionStore{loadErr: fmt.Errorf("database unavailable")}
	manager, err := New(Options{Session: store})
	if err != nil {
		testingT.Fatal(err)
	}
	defer manager.Close()
	status := manager.Status()
	if status.State != StateActionNeeded {
		testingT.Fatalf("store failure did not surface as action needed: %+v", status)
	}
}

func TestEnvCredentialsValidateLikeFileCredentials(testingT *testing.T) {
	valid, err := EnvCredentials("  fixture-user ", "fixture-secret").Credentials(context.Background())
	if err != nil || valid.Username != "fixture-user" || valid.Password != "fixture-secret" {
		testingT.Fatalf("unexpected env credentials: %+v err=%v", valid, err)
	}
	if _, err := EnvCredentials("", "fixture-secret").Credentials(context.Background()); err == nil {
		testingT.Fatal("empty username accepted")
	}
	if _, err := EnvCredentials("13800138000", "fixture-secret").Credentials(context.Background()); err == nil {
		testingT.Fatal("phone username accepted")
	}
}
