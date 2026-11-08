package sqlite

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/roiding/shadowflow/internal/thsauth"
)

type thsReloadTransport func(*http.Request) (*http.Response, error)

func (transport thsReloadTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestTHSLoginReloadAcrossDatabaseConnections(testingT *testing.T) {
	databasePath := filepath.Join(testingT.TempDir(), "sessions.db")
	serverStore, err := Open(databasePath)
	if err != nil {
		testingT.Fatal(err)
	}
	defer serverStore.Close()
	manualStore, err := Open(databasePath)
	if err != nil {
		testingT.Fatal(err)
	}
	defer manualStore.Close()
	transport := thsReloadTransport(func(request *http.Request) (*http.Response, error) {
		status := http.StatusUnauthorized
		body := `{"status_code":401}`
		if cookie, err := request.Cookie("ticket"); err == nil && cookie.Value == "fixture-shared-session" {
			status = http.StatusOK
			body = `{"status_code":0,"data":{}}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	var serverLogins atomic.Int32
	server, err := thsauth.New(thsauth.Options{Session: serverStore.THSSessionStore(), AutoLogin: true,
		Transport: transport, Login: func(context.Context, *http.Client) error {
			serverLogins.Add(1)
			return thsauth.ErrChallenge
		}})
	if err != nil {
		testingT.Fatal(err)
	}
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, "https://dataq.10jqka.com.cn/fetch-data-server/fetch/v1/specific_data", strings.NewReader("{}"))
	if err != nil {
		testingT.Fatal(err)
	}
	if _, err := server.Do(request); !errors.Is(err, thsauth.ErrChallenge) {
		testingT.Fatalf("server did not enter challenge state: %v", err)
	}
	manual, err := thsauth.New(thsauth.Options{Session: manualStore.THSSessionStore(), Transport: transport,
		Login: func(_ context.Context, client *http.Client) error {
			client.Jar.SetCookies(&url.URL{Scheme: "https", Host: "10jqka.com.cn", Path: "/"}, []*http.Cookie{{
				Name: "ticket", Value: "fixture-shared-session", Domain: ".10jqka.com.cn", Path: "/", Secure: true,
			}})
			return nil
		}})
	if err != nil {
		testingT.Fatal(err)
	}
	defer manual.Close()
	if err := manual.Login(context.Background()); err != nil {
		testingT.Fatal(err)
	}
	response, err := server.Do(request)
	if err != nil {
		testingT.Fatalf("server failed to adopt the login from another database connection: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || server.Status().State != thsauth.StateReady || serverLogins.Load() != 1 {
		testingT.Fatal("server re-ran password login instead of adopting the shared session")
	}
}
