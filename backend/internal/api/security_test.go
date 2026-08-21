package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/roiding/shadowflow/internal/repository/sqlite"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
)

func newSecurityServer(t *testing.T, options Options) (*Server, func()) {
	t.Helper()
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	calendar, err := tradingcalendar.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(store, calendar, slog.New(slog.NewTextHandler(os.Stderr, nil)), options)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return server, func() { store.Close() }
}

func TestBearerTokenProtectsAPIAndMetrics(t *testing.T) {
	server, closeStore := newSecurityServer(t, Options{APIToken: "secret"})
	defer closeStore()
	handler := server.Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API: got %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metrics: got %d", response.Code)
	}
	for _, target := range []string{"/health/live", "/health/ready"} {
		request = httptest.NewRequest(http.MethodGet, target, nil)
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s expected public: got %d", target, response.Code)
		}
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	request.Header.Set("Authorization", "Bearer wrong")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", response.Code)
	}
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid token: got %d body=%s", response.Code, response.Body.String())
	}
}

func TestRateLimiterReturns429(t *testing.T) {
	server, closeStore := newSecurityServer(t, Options{NormalRatePerMinute: 1})
	defer closeStore()
	handler := server.Handler()
	targets := []int{}
	for range 2 {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/trading-days?from=2026-08-01&to=2026-08-02", nil))
		targets = append(targets, response.Code)
	}
	if targets[0] != http.StatusOK || targets[1] != http.StatusTooManyRequests {
		t.Fatalf("rate limits: %v", targets)
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	server, closeStore := newSecurityServer(t, Options{APIToken: "secret"})
	defer closeStore()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	request.Header.Set("Authorization", "bearer secret")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("lowercase bearer scheme: got %d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthenticatedResponsesDisableCaching(t *testing.T) {
	server, closeStore := newSecurityServer(t, Options{APIToken: "secret"})
	defer closeStore()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q, want no-store", got)
	}
}

func TestRateLimiterDoesNotTrustForwardedForFromClients(t *testing.T) {
	server, closeStore := newSecurityServer(t, Options{NormalRatePerMinute: 1})
	defer closeStore()
	handler := server.Handler()
	first := httptest.NewRequest(http.MethodGet, "/api/v1/trading-days?from=2026-08-01&to=2026-08-02", nil)
	first.Header.Set("X-Forwarded-For", "198.51.100.1")
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, first)
	second := httptest.NewRequest(http.MethodGet, "/api/v1/trading-days?from=2026-08-01&to=2026-08-02", nil)
	second.Header.Set("X-Forwarded-For", "198.51.100.2")
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondResponse, second)
	if firstResponse.Code != http.StatusOK || secondResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("untrusted forwarded IP bypassed limiter: first=%d second=%d", firstResponse.Code, secondResponse.Code)
	}
}
