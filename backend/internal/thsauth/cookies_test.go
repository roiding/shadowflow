package thsauth

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type cookieTestTransport func(*http.Request) (*http.Response, error)

func (transport cookieTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func cookieTestResponse(request *http.Request, status int, body string) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode:    status,
		Status:        http.StatusText(status),
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       request,
	}
}

func cookieTestValue(request *http.Request, name string) string {
	cookie, cookieError := request.Cookie(name)
	if cookieError != nil {
		return ""
	}
	return cookie.Value
}

func cookieTestSet(client *http.Client, cookie *http.Cookie) {
	cloned := *cookie
	client.Jar.SetCookies(&url.URL{
		Scheme: "https",
		Host:   strings.TrimPrefix(cloned.Domain, "."),
		Path:   "/",
	}, []*http.Cookie{&cloned})
}

func cookieTestRequest(testingT *testing.T, method string, target string, body string) *http.Request {
	testingT.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, requestError := http.NewRequestWithContext(context.Background(), method, target, reader)
	if requestError != nil {
		testingT.Fatal(requestError)
	}
	request.Header.Set("Accept", "application/json")
	return request
}

func cookieTestReadResponse(testingT *testing.T, response *http.Response) string {
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

func cookieTestFind(testingT *testing.T, cookies []*http.Cookie, domain string, path string, name string) *http.Cookie {
	testingT.Helper()
	normalizedDomain := strings.TrimPrefix(domain, ".")
	for _, cookie := range cookies {
		if cookie != nil &&
			strings.TrimPrefix(cookie.Domain, ".") == normalizedDomain &&
			cookie.Path == path &&
			cookie.Name == name {
			return cookie
		}
	}
	testingT.Fatalf("cookie %s was not found for %s%s", name, domain, path)
	return nil
}

func cookieTestHasName(cookies []*http.Cookie, name string) bool {
	for _, cookie := range cookies {
		if cookie != nil && cookie.Name == name {
			return true
		}
	}
	return false
}

func cookieTestHasValue(cookies []*http.Cookie, name string, value string) bool {
	for _, cookie := range cookies {
		if cookie != nil && cookie.Name == name && cookie.Value == value {
			return true
		}
	}
	return false
}

func cookieTestMutationTime(testingT *testing.T, path string) time.Time {
	testingT.Helper()
	info, statError := os.Stat(path)
	if statError != nil {
		testingT.Fatal(statError)
	}
	return info.ModTime()
}

func cookieTestWaitAfter(testingT *testing.T, previous time.Time) {
	testingT.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if time.Now().After(previous) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	testingT.Fatal("system clock did not advance")
}

func TestSessionFileRoundTripPreservesCookieAttributes(testingT *testing.T) {
	directory := testingT.TempDir()
	if chmodError := os.Chmod(directory, 0o700); chmodError != nil {
		testingT.Fatal(chmodError)
	}
	path := filepath.Join(directory, "session.json")
	expiry := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	cookies := []*http.Cookie{
		{
			Name:     "ticket",
			Value:    "roundtrip-ticket",
			Path:     "/mobileapi",
			Domain:   ".10jqka.com.cn",
			Expires:  expiry,
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   172800,
		},
		{
			Name:     "csrf",
			Value:    "roundtrip-csrf",
			Path:     "/",
			Domain:   "dataq.10jqka.com.cn",
			Secure:   true,
			HttpOnly: false,
		},
	}
	if writeError := writeSession(path, cookies); writeError != nil {
		testingT.Fatalf("write session: %v", writeError)
	}
	restored, present, readError := readSession(path)
	if readError != nil || !present {
		testingT.Fatalf("read session: present=%t error=%v", present, readError)
	}
	if len(restored) != len(cookies) {
		testingT.Fatalf("expected %d cookies, got %d", len(cookies), len(restored))
	}
	ticket := cookieTestFind(testingT, restored, ".10jqka.com.cn", "/mobileapi", "ticket")
	if ticket.Value != "roundtrip-ticket" || !ticket.Secure || !ticket.HttpOnly ||
		ticket.SameSite != http.SameSiteLaxMode || ticket.MaxAge != 172800 {
		testingT.Fatalf("ticket attributes were not restored: %+v", ticket)
	}
	if !ticket.Expires.Equal(expiry) {
		testingT.Fatalf("ticket expiry mismatch: got %s want %s", ticket.Expires, expiry)
	}
	csrf := cookieTestFind(testingT, restored, "dataq.10jqka.com.cn", "/", "csrf")
	if csrf.Value != "roundtrip-csrf" || !csrf.Secure || csrf.HttpOnly {
		testingT.Fatalf("csrf attributes were not restored: %+v", csrf)
	}
	if !csrf.Expires.IsZero() {
		testingT.Fatal("session cookie unexpectedly acquired an expiry")
	}
	info, statError := os.Stat(path)
	if statError != nil {
		testingT.Fatal(statError)
	}
	if info.Mode().Perm() != 0o600 {
		testingT.Fatalf("session file permissions are %o", info.Mode().Perm())
	}
}

func TestResponseCookieRotationAndDeletionArePersisted(testingT *testing.T) {
	directory := testingT.TempDir()
	if chmodError := os.Chmod(directory, 0o700); chmodError != nil {
		testingT.Fatal(chmodError)
	}
	sessionPath := filepath.Join(directory, "session.json")
	oldExpiry := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	newExpiry := time.Now().Add(96 * time.Hour).UTC().Truncate(time.Second)
	if writeError := writeSession(sessionPath, []*http.Cookie{
		{
			Name:     "ticket",
			Value:    "response-old",
			Domain:   ".10jqka.com.cn",
			Path:     "/",
			Expires:  oldExpiry,
			Secure:   true,
			HttpOnly: true,
		},
		{
			Name:   "obsolete",
			Value:  "remove-me",
			Domain: ".10jqka.com.cn",
			Path:   "/",
			Secure: true,
		},
	}); writeError != nil {
		testingT.Fatal(writeError)
	}
	transport := cookieTestTransport(func(request *http.Request) (*http.Response, error) {
		response := cookieTestResponse(request, http.StatusOK, `{"status_code":0,"data":{}}`)
		response.Header.Add("Set-Cookie",
			"ticket=response-new; Domain=.10jqka.com.cn; Path=/; Expires="+newExpiry.Format(http.TimeFormat)+"; Secure; HttpOnly")
		response.Header.Add("Set-Cookie",
			"obsolete=; Domain=.10jqka.com.cn; Path=/; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Secure")
		return response, nil
	})
	manager, createError := New(Options{
		SessionFile: sessionPath,
		Transport:   transport,
	})
	if createError != nil {
		testingT.Fatalf("create manager: %v", createError)
	}
	defer manager.Close()
	response, responseError := manager.Do(cookieTestRequest(
		testingT,
		http.MethodGet,
		"https://data.10jqka.com.cn/mobileapi/hotspot_focus/chart_config/v1/query_config",
		"",
	))
	if responseError != nil {
		testingT.Fatalf("response cookie request failed: %v", responseError)
	}
	_ = cookieTestReadResponse(testingT, response)
	restored, present, readError := readSession(sessionPath)
	if readError != nil || !present {
		testingT.Fatalf("read rotated session: present=%t error=%v", present, readError)
	}
	if len(restored) != 1 {
		testingT.Fatalf("deleted response cookie was persisted: %d cookies", len(restored))
	}
	ticket := cookieTestFind(testingT, restored, ".10jqka.com.cn", "/", "ticket")
	if ticket.Value != "response-new" || !ticket.Secure || !ticket.HttpOnly {
		testingT.Fatalf("response cookie rotation was not persisted: %+v", ticket)
	}
	if !ticket.Expires.Equal(newExpiry) {
		testingT.Fatalf("rotated response expiry mismatch: got %s want %s", ticket.Expires, newExpiry)
	}
	if status := manager.Status(); status.ExpiresAt == nil || !status.ExpiresAt.Equal(newExpiry) {
		testingT.Fatalf("manager expiry did not follow response rotation: %+v", status)
	}
	before := cookieTestMutationTime(testingT, sessionPath)
	cookieTestWaitAfter(testingT, before)
	reloaded, reloadError := New(Options{SessionFile: sessionPath, Transport: transport})
	if reloadError != nil {
		testingT.Fatalf("reload session: %v", reloadError)
	}
	reloadedResponse, reloadedError := reloaded.Do(cookieTestRequest(
		testingT,
		http.MethodGet,
		"https://data.10jqka.com.cn/mobileapi/hotspot_focus/chart_config/v1/query_config",
		"",
	))
	if reloadedError != nil {
		testingT.Fatalf("reloaded session request failed: %v", reloadedError)
	}
	_ = cookieTestReadResponse(testingT, reloadedResponse)
	if closeError := reloaded.Close(); closeError != nil {
		testingT.Fatalf("close reloaded manager: %v", closeError)
	}
	after := cookieTestMutationTime(testingT, sessionPath)
	if after.After(before) {
		testingT.Fatalf("deleted response cookie was resurrected and rewritten: before=%s after=%s", before, after)
	}
}

func TestHostOnlyCookieDoesNotExpandToSubdomain(testingT *testing.T) {
	jar, createError := newCookieJar()
	if createError != nil {
		testingT.Fatal(createError)
	}
	jar.SetCookies(mustParseURL("https://data.10jqka.com.cn/"), []*http.Cookie{{
		Name:     "host_only",
		Value:    "parent-only",
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
	}})
	cookies, _ := jar.snapshot()
	hostOnly := cookieTestFind(testingT, cookies, "data.10jqka.com.cn", "/", "host_only")
	if hostOnly.Domain != "data.10jqka.com.cn" {
		testingT.Fatalf("host-only cookie was widened to %q", hostOnly.Domain)
	}
	parentCookies := jar.Cookies(mustParseURL("https://data.10jqka.com.cn/"))
	if !cookieTestHasValue(parentCookies, "host_only", "parent-only") {
		testingT.Fatal("host-only cookie was not sent to its origin host")
	}
	childCookies := jar.Cookies(mustParseURL("https://child.data.10jqka.com.cn/"))
	if cookieTestHasName(childCookies, "host_only") {
		testingT.Fatal("host-only cookie leaked to a subdomain")
	}
	directory := testingT.TempDir()
	if chmodError := os.Chmod(directory, 0o700); chmodError != nil {
		testingT.Fatal(chmodError)
	}
	sessionPath := filepath.Join(directory, "session.json")
	if writeError := writeSession(sessionPath, cookies); writeError != nil {
		testingT.Fatal(writeError)
	}
	restored, present, readError := readSession(sessionPath)
	if readError != nil || !present {
		testingT.Fatalf("read host-only session: present=%t error=%v", present, readError)
	}
	restoredHostOnly := cookieTestFind(testingT, restored, "data.10jqka.com.cn", "/", "host_only")
	restoredJar, installError := newCookieJar()
	if installError != nil {
		testingT.Fatal(installError)
	}
	if installError := installCookies(restoredJar, restored); installError != nil {
		testingT.Fatal(installError)
	}
	installedCookies, _ := restoredJar.snapshot()
	installedHostOnly := cookieTestFind(testingT, installedCookies, "data.10jqka.com.cn", "/", "host_only")
	if restoredHostOnly.Domain != "data.10jqka.com.cn" || installedHostOnly.Domain != "data.10jqka.com.cn" {
		testingT.Fatalf("host-only domain changed across persistence: restored=%q installed=%q",
			restoredHostOnly.Domain, installedHostOnly.Domain)
	}
	if cookieTestHasName(restoredJar.Cookies(mustParseURL("https://child.data.10jqka.com.cn/")), "host_only") {
		testingT.Fatal("restored host-only cookie leaked to a subdomain")
	}
	jar.SetCookies(mustParseURL("https://data.10jqka.com.cn/"), []*http.Cookie{{
		Name:   "domain_wide",
		Value:  "shared",
		Domain: ".data.10jqka.com.cn",
		Path:   "/",
		Secure: true,
	}})
	childCookies = jar.Cookies(mustParseURL("https://child.data.10jqka.com.cn/"))
	if !cookieTestHasValue(childCookies, "domain_wide", "shared") {
		testingT.Fatal("domain cookie was not sent to an approved subdomain")
	}
}

func TestCookieMaxAgeIsStoredAsAbsoluteExpiry(testingT *testing.T) {
	jar, createError := newCookieJar()
	if createError != nil {
		testingT.Fatal(createError)
	}
	jar.SetCookies(mustParseURL("https://data.10jqka.com.cn/"), []*http.Cookie{{
		Name:     "short_lived",
		Value:    "bounded",
		Domain:   ".10jqka.com.cn",
		Path:     "/",
		MaxAge:   3600,
		Secure:   true,
		HttpOnly: true,
	}})
	cookies, _ := jar.snapshot()
	stored := cookieTestFind(testingT, cookies, ".10jqka.com.cn", "/", "short_lived")
	if stored.MaxAge != 0 || stored.Expires.IsZero() {
		testingT.Fatalf("MaxAge was not converted to an absolute expiry: %+v", stored)
	}
	remaining := time.Until(stored.Expires)
	if remaining < 3595*time.Second || remaining > 3605*time.Second {
		testingT.Fatalf("MaxAge expiry is not bounded by its original lifetime: %s", remaining)
	}
	directory := testingT.TempDir()
	if chmodError := os.Chmod(directory, 0o700); chmodError != nil {
		testingT.Fatal(chmodError)
	}
	sessionPath := filepath.Join(directory, "session.json")
	if writeError := writeSession(sessionPath, cookies); writeError != nil {
		testingT.Fatal(writeError)
	}
	restored, present, readError := readSession(sessionPath)
	if readError != nil || !present {
		testingT.Fatalf("reload MaxAge session: present=%t error=%v", present, readError)
	}
	reloaded := cookieTestFind(testingT, restored, ".10jqka.com.cn", "/", "short_lived")
	if reloaded.MaxAge != 0 || !reloaded.Expires.Equal(stored.Expires) {
		testingT.Fatalf("MaxAge was extended across restart: before=%+v after=%+v", stored, reloaded)
	}
	installedJar, installError := newCookieJar()
	if installError != nil {
		testingT.Fatal(installError)
	}
	if installError := installCookies(installedJar, restored); installError != nil {
		testingT.Fatal(installError)
	}
	installedCookies, _ := installedJar.snapshot()
	installed := cookieTestFind(testingT, installedCookies, ".10jqka.com.cn", "/", "short_lived")
	if installed.MaxAge != 0 || !installed.Expires.Equal(stored.Expires) {
		testingT.Fatalf("restored MaxAge was allowed to renew: before=%+v after=%+v", stored, installed)
	}
}

func TestTrackedJarSnapshotRetainsScopedMetadata(testingT *testing.T) {
	jar, createError := newCookieJar()
	if createError != nil {
		testingT.Fatal(createError)
	}
	expiry := time.Now().Add(12 * time.Hour).UTC().Truncate(time.Second)
	cookies := []*http.Cookie{
		{
			Name:     "scoped",
			Value:    "metadata",
			Domain:   ".10jqka.com.cn",
			Path:     "/mobileapi",
			Expires:  expiry,
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		},
		{
			Name:     "host_only",
			Value:    "origin-bound",
			Domain:   "dataq.10jqka.com.cn",
			Path:     "/",
			Secure:   true,
			HttpOnly: true,
		},
	}
	for _, cookie := range cookies {
		jar.SetCookies(
			mustParseURL("https://"+strings.TrimPrefix(cookie.Domain, ".")+"/"),
			[]*http.Cookie{{Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: cookie.Path}},
		)
	}
	if installError := installCookies(jar, cookies); installError != nil {
		testingT.Fatal(installError)
	}
	snapshot, _ := jar.snapshot()
	scoped := cookieTestFind(testingT, snapshot, ".10jqka.com.cn", "/mobileapi", "scoped")
	if scoped.Value != "metadata" || !scoped.Secure || !scoped.HttpOnly ||
		scoped.SameSite != http.SameSiteStrictMode || !scoped.Expires.Equal(expiry) {
		testingT.Fatalf("snapshot lost scoped metadata: %+v", scoped)
	}
	hostOnly := cookieTestFind(testingT, snapshot, "dataq.10jqka.com.cn", "/", "host_only")
	if hostOnly.Domain != "dataq.10jqka.com.cn" {
		testingT.Fatalf("host-only snapshot domain was widened: %+v", hostOnly)
	}
	if !cookieTestHasName(jar.Cookies(mustParseURL("https://dataq.10jqka.com.cn/")), "host_only") {
		testingT.Fatal("host-only cookie was not installed on its origin")
	}
	if cookieTestHasName(jar.Cookies(mustParseURL("https://child.dataq.10jqka.com.cn/")), "host_only") {
		testingT.Fatal("host-only snapshot cookie leaked to a subdomain")
	}
}

func TestManagerRestoresPersistedCookieScopeAndExpiry(testingT *testing.T) {
	directory := testingT.TempDir()
	if chmodError := os.Chmod(directory, 0o700); chmodError != nil {
		testingT.Fatal(chmodError)
	}
	sessionPath := filepath.Join(directory, "session.json")
	expiry := time.Now().Add(36 * time.Hour).UTC().Truncate(time.Second)
	cookies := []*http.Cookie{{
		Name:     "ticket",
		Value:    "restored-ticket",
		Domain:   ".10jqka.com.cn",
		Path:     "/mobileapi",
		Expires:  expiry,
		Secure:   true,
		HttpOnly: true,
	}}
	if writeError := writeSession(sessionPath, cookies); writeError != nil {
		testingT.Fatal(writeError)
	}
	transport := cookieTestTransport(func(request *http.Request) (*http.Response, error) {
		if cookieTestValue(request, "ticket") != "restored-ticket" {
			return cookieTestResponse(request, http.StatusUnauthorized, `{"error":"cookie was not restored"}`), nil
		}
		return cookieTestResponse(request, http.StatusOK, `{"status_code":0,"data":{}}`), nil
	})
	manager, createError := New(Options{
		SessionFile: sessionPath,
		Transport:   transport,
	})
	if createError != nil {
		testingT.Fatalf("create manager: %v", createError)
	}
	defer manager.Close()
	status := manager.Status()
	if status.State != StateReady {
		testingT.Fatalf("persisted session was not restored: %+v", status)
	}
	if status.ExpiresAt == nil || !status.ExpiresAt.Equal(expiry) {
		testingT.Fatalf("restored expiry mismatch: got %v want %s", status.ExpiresAt, expiry)
	}
	response, responseError := manager.Do(cookieTestRequest(
		testingT,
		http.MethodGet,
		"https://data.10jqka.com.cn/mobileapi/hotspot_focus/chart_config/v1/query_config",
		"",
	))
	if responseError != nil {
		testingT.Fatalf("restored request failed: %v", responseError)
	}
	result := cookieTestReadResponse(testingT, response)
	if !strings.Contains(result, `"status_code":0`) {
		testingT.Fatalf("restored cookie was not sent with expected scope: %q", result)
	}
}

func TestLoginCookieRotationPersistsNewValueAndMetadata(testingT *testing.T) {
	directory := testingT.TempDir()
	if chmodError := os.Chmod(directory, 0o700); chmodError != nil {
		testingT.Fatal(chmodError)
	}
	sessionPath := filepath.Join(directory, "session.json")
	oldExpiry := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	newExpiry := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	oldSession := []*http.Cookie{
		{
			Name:     "ticket",
			Value:    "old-ticket",
			Domain:   ".10jqka.com.cn",
			Path:     "/",
			Expires:  oldExpiry,
			Secure:   true,
			HttpOnly: true,
		},
		{
			Name:   "csrf",
			Value:  "old-csrf",
			Domain: ".10jqka.com.cn",
			Path:   "/",
			Secure: true,
		},
	}
	if writeError := writeSession(sessionPath, oldSession); writeError != nil {
		testingT.Fatal(writeError)
	}
	logins := 0
	login := func(context.Context, *http.Client) error {
		logins++
		return nil
	}
	transport := cookieTestTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/fetch-data-server/fetch/v1/specific_data":
			if _, readError := io.ReadAll(request.Body); readError != nil {
				return nil, readError
			}
			ticket := cookieTestValue(request, "ticket")
			switch ticket {
			case "old-ticket":
				return cookieTestResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
			case "new-ticket":
				return cookieTestResponse(request, http.StatusOK, `{"status_code":0}`), nil
			default:
				return cookieTestResponse(request, http.StatusUnauthorized, `{"error":"unexpected ticket"}`), nil
			}
		case "/mobileapi/hotspot_focus/chart_config/v1/query_config":
			if cookieTestValue(request, "ticket") != "new-ticket" {
				return cookieTestResponse(request, http.StatusUnauthorized, `{"error":"rotated cookie missing"}`), nil
			}
			return cookieTestResponse(request, http.StatusOK, `{"status_code":0,"data":{}}`), nil
		default:
			return cookieTestResponse(request, http.StatusNotFound, `{"error":"unexpected path"}`), nil
		}
	})
	manager, createError := New(Options{
		SessionFile: sessionPath,
		AutoLogin:   true,
		Login: func(ctx context.Context, client *http.Client) error {
			if loginError := login(ctx, client); loginError != nil {
				return loginError
			}
			cookieTestSet(client, &http.Cookie{
				Name:     "ticket",
				Value:    "new-ticket",
				Domain:   ".10jqka.com.cn",
				Path:     "/",
				Expires:  newExpiry,
				Secure:   true,
				HttpOnly: true,
			})
			cookieTestSet(client, &http.Cookie{
				Name:   "csrf",
				Value:  "new-csrf",
				Domain: ".10jqka.com.cn",
				Path:   "/",
				Secure: true,
			})
			return nil
		},
		Transport: transport,
	})
	if createError != nil {
		testingT.Fatalf("create manager: %v", createError)
	}
	defer manager.Close()
	response, responseError := manager.Do(cookieTestRequest(
		testingT,
		http.MethodPost,
		"https://dataq.10jqka.com.cn/fetch-data-server/fetch/v1/specific_data",
		"symbol=600000",
	))
	if responseError != nil {
		testingT.Fatalf("rotated request failed: %v", responseError)
	}
	if body := cookieTestReadResponse(testingT, response); !strings.Contains(body, `"status_code":0`) {
		testingT.Fatalf("rotated session was not used for replay: %q", body)
	}
	if logins != 1 {
		testingT.Fatalf("expected one login, got %d", logins)
	}
	restored, present, readError := readSession(sessionPath)
	if readError != nil || !present {
		testingT.Fatalf("re-read session: present=%t error=%v", present, readError)
	}
	if len(restored) != 2 {
		testingT.Fatalf("expected rotated cookie set of 2, got %d", len(restored))
	}
	ticket := cookieTestFind(testingT, restored, ".10jqka.com.cn", "/", "ticket")
	if ticket.Value != "new-ticket" || !ticket.Secure || !ticket.HttpOnly {
		testingT.Fatalf("rotated ticket metadata was not persisted: %+v", ticket)
	}
	if !ticket.Expires.Equal(newExpiry) {
		testingT.Fatalf("rotated ticket expiry mismatch: got %s want %s", ticket.Expires, newExpiry)
	}
	csrf := cookieTestFind(testingT, restored, ".10jqka.com.cn", "/", "csrf")
	if csrf.Value != "new-csrf" || !csrf.Secure {
		testingT.Fatalf("rotated csrf metadata was not persisted: %+v", csrf)
	}
	if status := manager.Status(); status.ExpiresAt == nil || !status.ExpiresAt.Equal(newExpiry) {
		testingT.Fatalf("manager expiry did not follow rotated cookie: %+v", status)
	}
}
