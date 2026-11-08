package thsauth

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type passwordTransport func(*http.Request) (*http.Response, error)

func (transport passwordTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func passwordFixtureCredentials(testingT *testing.T) string {
	testingT.Helper()
	directory := testingT.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		testingT.Fatal(err)
	}
	path := filepath.Join(directory, "credentials.json")
	if err := os.WriteFile(path, []byte(`{"username":"fixture-user","password":"fixture-secret"}`), 0o600); err != nil {
		testingT.Fatal(err)
	}
	return path
}

func writePasswordFixture(testingT *testing.T, body string) string {
	testingT.Helper()
	path := passwordFixtureCredentials(testingT)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		testingT.Fatal(err)
	}
	return path
}

func TestPasswordSaltMatchesOfficialJavaScript(testingT *testing.T) {
	salt := saltResponse{DSK: "fixture-dsk", DSV: "fixture-dsv", SSV: "UEYKUQhMR0RGUB9EBwgQ"}
	for attempt, expected := range map[int]string{
		1: "D1ZRUQZZDw4HBQYFUghWAAEHAQBWAAcDCQZSAgNUAAtSVVcDA1MDDwYJDw4HVgJUAl8EVAcHXlcCVVVYAg8EUw==",
		2: "DAZTUAYIWFZbAVYFAFNWAgEEBQRRVQhTAFsDAQBWCws=",
	} {
		actual, err := saltedPassword("4c7400aa9bc33b3a304a208e880bac23", "fixture-nonce", salt, attempt)
		if err != nil || actual != expected {
			testingT.Fatalf("salt step %d mismatch: %v", attempt, err)
		}
	}
	for _, bad := range []saltResponse{{}, {DSK: "key", DSV: "value", SSV: "!not-base64!"}, {DSK: "key", DSV: "value", SSV: "YQ=="}} {
		if _, err := saltedPassword("digest", "nonce", bad, 1); err == nil {
			testingT.Fatal("malformed salt accepted")
		}
	}
}

func TestPasswordLoginPreservesWhitespaceAndDoesNotLeakCredentials(testingT *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		testingT.Fatal(err)
	}
	keyDocument := fmt.Sprintf(`var thspubkey={"v":"fixture-key","m":"%X","p":"%X"};`, privateKey.N, privateKey.E)
	decrypt := func(encoded string) string {
		ciphertext, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			testingT.Fatal(err)
		}
		plaintext, err := rsa.DecryptPKCS1v15(rand.Reader, privateKey, ciphertext)
		if err != nil {
			testingT.Fatal(err)
		}
		return string(plaintext)
	}
	const password = "  fixture-secret  "
	jar, _ := cookiejar.New(nil)
	loginCalls, saltCalls := 0, 0
	salt := saltResponse{DSK: "fixture-dsk", DSV: "fixture-dsv"}
	var nonce string
	client := &http.Client{Jar: jar, Transport: passwordTransport(func(request *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: request}
		payload := ""
		switch request.URL.Path {
		case "/login":
			payload = "<html><title>official login fixture</title></html>"
		case "/pubkey/default.js":
			payload = keyDocument
		case "/user/getGS":
			saltCalls++
			body, _ := io.ReadAll(request.Body)
			values, _ := url.ParseQuery(string(body))
			if decrypt(values.Get("uname")) != "fixture-user" || values.Get("rsa_version") != "fixture-key" {
				testingT.Fatal("invalid encrypted username or key version")
			}
			nonce = values.Get("crnd")
			salt.SSV = base64.StdEncoding.EncodeToString(xorBytes([]byte("gs=fixture-salt"), []byte(sha256Hex(nonce+salt.DSK))))
			payload = fmt.Sprintf(`{"errorcode":0,"dsk":%q,"dsv":%q,"ssv":%q}`, salt.DSK, salt.DSV, salt.SSV)
		case "/login/dologinreturnjson2":
			loginCalls++
			body, _ := io.ReadAll(request.Body)
			values, _ := url.ParseQuery(string(body))
			digest := md5.Sum([]byte(password))
			plainDigest := hex.EncodeToString(digest[:])
			trimmedDigest := md5.Sum([]byte(strings.TrimSpace(password)))
			expected, _ := saltedPassword(plainDigest, nonce, salt, loginCalls)
			if decrypt(values.Get("passwd")) != plainDigest || decrypt(values.Get("passwd")) == hex.EncodeToString(trimmedDigest[:]) {
				testingT.Fatal("password whitespace was not preserved")
			}
			if decrypt(values.Get("passwdSalt")) != expected || values.Get("saltLoginTimes") != strconv.Itoa(loginCalls) || values.Get("crnd") != nonce || values.Get("dsk") != salt.DSK {
				testingT.Fatal("salt challenge state was lost")
			}
			payload = `{"errorcode":0}`
			response.Header.Set("Set-Cookie", "ticket=fixture-session; Domain=.10jqka.com.cn; Path=/; Secure; HttpOnly")
		case "/mobileapi/hotspot_focus/chart_config/v1/query_config":
			payload = `{"status_code":0,"data":{"chart_list":[]}}`
		default:
			testingT.Fatalf("unexpected login request path %s", request.URL.Path)
		}
		response.Body = io.NopCloser(strings.NewReader(payload))
		return response, nil
	})}
	path := writePasswordFixture(testingT, `{"username":"  fixture-user  ","password":"  fixture-secret  "}`)
	credentials, err := LoadCredentials(path)
	if err != nil {
		testingT.Fatal(err)
	}
	if credentials.Username != "fixture-user" || credentials.Password != password {
		testingT.Fatalf("unexpected credential normalization: username=%q password_length=%d", credentials.Username, len(credentials.Password))
	}
	if err := PasswordLogin(path)(context.Background(), client); err != nil {
		testingT.Fatal(err)
	}
	if saltCalls != 1 || loginCalls != 1 {
		testingT.Fatalf("unexpected password exchange: salt=%d login=%d", saltCalls, loginCalls)
	}
}

func TestPasswordLoginOfficialExchangeAndChallengeStop(testingT *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		testingT.Fatal(err)
	}
	keyDocument := fmt.Sprintf(`var thspubkey={"v":"fixture-key","m":"%X","p":"%X"};`, privateKey.N, privateKey.E)
	decrypt := func(encoded string) string {
		ciphertext, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			testingT.Fatal(err)
		}
		plaintext, err := rsa.DecryptPKCS1v15(rand.Reader, privateKey, ciphertext)
		if err != nil {
			testingT.Fatal(err)
		}
		return string(plaintext)
	}
	for _, scenario := range []struct {
		name          string
		code          int
		second        bool
		challengeSalt bool
		gateway401    bool
		want          error
		verified      bool
		loginCalls    int
		verifyCalls   int
	}{
		{name: "success", verified: true, loginCalls: 1, verifyCalls: 1},
		{name: "gateway cookie handshake", gateway401: true, verified: true, loginCalls: 1, verifyCalls: 1},
		{name: "second salt exchange", second: true, verified: true, loginCalls: 2, verifyCalls: 1},
		{name: "account refusal", code: -10501, want: ErrChallenge, loginCalls: 1},
		{name: "device verification", code: -10510, want: ErrChallenge, loginCalls: 1},
		{name: "weak password continuation", code: -10528, want: ErrChallenge, loginCalls: 1},
		{name: "weak password change required", code: -10527, want: ErrChallenge, loginCalls: 1},
		{name: "wrong password", code: -11032, want: ErrCredentials, loginCalls: 1},
		{name: "wrong password with challenge salt", code: -11032, challengeSalt: true, want: ErrCredentials, loginCalls: 1},
		{name: "success without authenticated session", want: ErrAuthExpired, loginCalls: 1, verifyCalls: 1},
	} {
		testingT.Run(scenario.name, func(testingT *testing.T) {
			jar, _ := cookiejar.New(nil)
			loginCalls, verifyCalls, loginPageCalls := 0, 0, 0
			salt := saltResponse{DSK: "fixture-dsk", DSV: "fixture-dsv"}
			var nonce string
			client := &http.Client{Jar: jar, Transport: passwordTransport(func(request *http.Request) (*http.Response, error) {
				response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: request}
				payload := ""
				switch request.URL.Path {
				case "/login":
					loginPageCalls++
					if scenario.gateway401 && loginPageCalls == 1 {
						response.StatusCode = http.StatusUnauthorized
						response.Header.Set("Set-Cookie", "vvvv=1; Path=/; Secure")
					} else if scenario.gateway401 && !strings.Contains(request.Header.Get("Cookie"), "vvvv=1") {
						testingT.Fatal("gateway cookie was not replayed")
					}
					payload = "<html><title>official login fixture</title></html>"
				case "/pubkey/default.js":
					payload = keyDocument
				case "/user/getGS":
					body, _ := io.ReadAll(request.Body)
					values, _ := url.ParseQuery(string(body))
					if decrypt(values.Get("uname")) != "fixture-user" || values.Get("rsa_version") != "fixture-key" {
						testingT.Fatal("invalid encrypted username or key version")
					}
					nonce = values.Get("crnd")
					salt.SSV = base64.StdEncoding.EncodeToString(xorBytes([]byte("gs=fixture-salt"), []byte(sha256Hex(nonce+salt.DSK))))
					payload = fmt.Sprintf(`{"errorcode":0,"dsk":%q,"dsv":%q,"ssv":%q}`, salt.DSK, salt.DSV, salt.SSV)
				case "/login/dologinreturnjson2":
					loginCalls++
					body, _ := io.ReadAll(request.Body)
					if strings.Contains(string(body), "fixture-secret") || strings.Contains(string(body), "fixture-user") {
						testingT.Fatal("credentials were not encrypted")
					}
					values, _ := url.ParseQuery(string(body))
					if values.Get("saltLoginTimes") != strconv.Itoa(loginCalls) || values.Get("crnd") != nonce || values.Get("dsk") != salt.DSK {
						testingT.Fatal("salt challenge state was lost")
					}
					digest := md5.Sum([]byte("fixture-secret"))
					plainDigest := hex.EncodeToString(digest[:])
					expected, _ := saltedPassword(plainDigest, nonce, salt, loginCalls)
					if decrypt(values.Get("passwd")) != plainDigest || decrypt(values.Get("passwdSalt")) != expected {
						testingT.Fatal("password or salted proof mismatch")
					}
					if scenario.second && loginCalls == 1 {
						salt.DSK, salt.DSV = "second-dsk", "second-dsv"
						payload = `{"errorcode":-1,"dsk":"second-dsk","dsv":"second-dsv"}`
					} else if scenario.challengeSalt {
						payload = fmt.Sprintf(`{"errorcode":%d,"dsk":"challenge-dsk","dsv":"challenge-dsv","errormsg":"DO-NOT-LOG-fixture-secret"}`, scenario.code)
					} else if scenario.code == 0 {
						payload = `{"errorcode":0}`
						response.Header.Set("Set-Cookie", "ticket=fixture-session; Domain=.10jqka.com.cn; Path=/; Secure; HttpOnly")
					} else {
						payload = fmt.Sprintf(`{"errorcode":%d,"errormsg":"DO-NOT-LOG-fixture-secret"}`, scenario.code)
					}
				case "/mobileapi/hotspot_focus/chart_config/v1/query_config":
					verifyCalls++
					if request.URL.Hostname() != "data.10jqka.com.cn" || !strings.Contains(request.Header.Get("Cookie"), "ticket=fixture-session") {
						testingT.Fatal("new session was not verified on its data host")
					}
					payload = `{"status_code":-2,"status_msg":"需要授权"}`
					if scenario.verified {
						payload = `{"status_code":0,"data":{"chart_list":[]}}`
					}
				default:
					testingT.Fatalf("unexpected login request path %s", request.URL.Path)
				}
				response.Body = io.NopCloser(strings.NewReader(payload))
				return response, nil
			})}
			err := PasswordLogin(passwordFixtureCredentials(testingT))(context.Background(), client)
			if scenario.want == nil {
				if err != nil {
					testingT.Fatalf("unexpected password error: %v", err)
				}
			} else if !errors.Is(err, scenario.want) {
				testingT.Fatalf("wrong password error: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "fixture-secret") {
				testingT.Fatalf("password leaked in error: %v", err)
			}
			if loginPageCalls != 0 || loginCalls != scenario.loginCalls || verifyCalls != scenario.verifyCalls {
				testingT.Fatalf("unexpected password retries: login_page=%d login=%d verification=%d", loginPageCalls, loginCalls, verifyCalls)
			}
		})
	}
}

func TestPasswordGatewayRetryIsBoundedAndChallengeClassified(testingT *testing.T) {
	jar, _ := cookiejar.New(nil)
	requests := 0
	client := &http.Client{Jar: jar, Transport: passwordTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		response := &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Request: request}
		response.Header.Set("Set-Cookie", "vvvv=1; Path=/; Secure")
		response.Body = io.NopCloser(strings.NewReader("fixture gateway response"))
		return response, nil
	})}
	if _, err := passwordRequest(context.Background(), client, LoginURL, nil); !errors.Is(err, ErrChallenge) {
		testingT.Fatalf("unexpected gateway error: %v", err)
	}
	if requests != 2 {
		testingT.Fatalf("unexpected gateway retries: %d", requests)
	}
}

func TestPasswordGatewayFailureDoesNotReplayCredentialPost(testingT *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests} {
		testingT.Run(http.StatusText(status), func(testingT *testing.T) {
			jar, _ := cookiejar.New(nil)
			requests := 0
			client := &http.Client{Jar: jar, Transport: passwordTransport(func(request *http.Request) (*http.Response, error) {
				requests++
				response := &http.Response{StatusCode: status, Header: make(http.Header), Request: request}
				response.Body = io.NopCloser(strings.NewReader("fixture gateway response"))
				return response, nil
			})}
			if _, err := passwordRequest(context.Background(), client, LoginURL, url.Values{"fixture": {"value"}}); !errors.Is(err, ErrChallenge) {
				testingT.Fatalf("unexpected POST gateway error: %v", err)
			}
			if requests != 1 {
				testingT.Fatalf("credential-bearing POST was replayed: %d", requests)
			}
		})
	}
}

func TestPasswordErrorClassificationMatchesOfficialJavaScript(testingT *testing.T) {
	for _, code := range []int{-10507, -11400, -10501, -10510, -10528, -10527} {
		if !isInteractivePasswordCode(code) || !errors.Is(passwordError(code), ErrChallenge) {
			testingT.Fatalf("official interactive password code %d was misclassified", code)
		}
	}
	if isInteractivePasswordCode(-10511) {
		testingT.Fatal("SMS refusal code was classified as a password interaction")
	}
	if !errors.Is(passwordError(-11032), ErrCredentials) {
		testingT.Fatal("wrong-password code was misclassified")
	}
}

func TestCredentialsRejectUnsafeFilesAndUnsupportedAccounts(testingT *testing.T) {
	if _, err := LoadCredentials(""); !errors.Is(err, ErrUnconfigured) {
		testingT.Fatal("empty credential path was not unconfigured")
	}
	if _, err := LoadCredentials("relative.json"); !errors.Is(err, ErrCredentials) {
		testingT.Fatal("relative credential path accepted")
	}
	missingDirectory := testingT.TempDir()
	if err := os.Chmod(missingDirectory, 0o700); err != nil {
		testingT.Fatal(err)
	}
	if _, err := LoadCredentials(filepath.Join(missingDirectory, "missing.json")); !errors.Is(err, ErrUnconfigured) {
		testingT.Fatal("missing private credentials were not unconfigured")
	}
	path := passwordFixtureCredentials(testingT)
	credentials, err := LoadCredentials(path)
	if err != nil {
		testingT.Fatal(err)
	}
	if credentials != (Credentials{Username: "fixture-user", Password: "fixture-secret"}) {
		testingT.Fatal("fixture credentials changed")
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		testingT.Fatal(err)
	}
	if _, err := LoadCredentials(path); !errors.Is(err, ErrCredentials) {
		testingT.Fatal("non-private credential directory accepted")
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		testingT.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		testingT.Fatal(err)
	}
	if _, err := LoadCredentials(path); !errors.Is(err, ErrCredentials) {
		testingT.Fatal("world-readable credentials accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		testingT.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(path), "link.json")
	if err := os.Symlink(path, link); err != nil {
		testingT.Fatal(err)
	}
	if _, err := LoadCredentials(link); !errors.Is(err, ErrCredentials) {
		testingT.Fatal("symlink credentials accepted")
	}
	directoryLink := filepath.Join(testingT.TempDir(), "credentials-directory-link")
	if err := os.Symlink(filepath.Dir(path), directoryLink); err != nil {
		testingT.Fatal(err)
	}
	if _, err := LoadCredentials(filepath.Join(directoryLink, filepath.Base(path))); !errors.Is(err, ErrCredentials) {
		testingT.Fatal("symlinked credential directory accepted")
	}
	for _, fixture := range []struct {
		name string
		body string
	}{
		{name: "empty username", body: `{"username":"","password":"fixture-secret"}`},
		{name: "whitespace username", body: `{"username":"   ","password":"fixture-secret"}`},
		{name: "phone username", body: `{"username":"13800000000","password":"fixture-secret"}`},
		{name: "email username", body: `{"username":"fixture@example.com","password":"fixture-secret"}`},
		{name: "empty password", body: `{"username":"fixture-user","password":""}`},
		{name: "unknown field", body: `{"username":"fixture-user","password":"fixture-secret","extra":true}`},
		{name: "trailing document", body: `{"username":"fixture-user","password":"fixture-secret"} {}`},
		{name: "invalid JSON", body: `{"username":"fixture-user","password":"fixture-secret"`},
		{name: "oversized username", body: fmt.Sprintf(`{"username":%q,"password":"fixture-secret"}`, strings.Repeat("u", 101))},
		{name: "oversized password", body: fmt.Sprintf(`{"username":"fixture-user","password":%q}`, strings.Repeat("p", 257))},
		{name: "oversized file", body: strings.Repeat("x", 8193)},
	} {
		if err := os.WriteFile(path, []byte(fixture.body), 0o600); err != nil {
			testingT.Fatal(err)
		}
		if _, err := LoadCredentials(path); !errors.Is(err, ErrCredentials) {
			testingT.Fatalf("%s was accepted", fixture.name)
		} else if strings.Contains(err.Error(), "fixture-secret") {
			testingT.Fatalf("%s leaked credentials: %v", fixture.name, err)
		}
	}
	spacesPath := writePasswordFixture(testingT, `{"username":"fixture-user","password":"   "}`)
	if credentials, err := LoadCredentials(spacesPath); err != nil || credentials.Password != "   " {
		testingT.Fatalf("whitespace-only password was not preserved: %v", err)
	}
	maxUsername := strings.Repeat("u", 100)
	maxPassword := strings.Repeat("p", 256)
	encoded, err := json.Marshal(Credentials{Username: maxUsername, Password: maxPassword})
	if err != nil {
		testingT.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		testingT.Fatal(err)
	}
	if credentials, err := LoadCredentials(path); err != nil || credentials.Username != maxUsername || credentials.Password != maxPassword {
		testingT.Fatalf("bounded credentials were rejected or changed: %v", err)
	}
	if _, err := parseLoginPublicKey([]byte(`var thspubkey={};alert("execute");`)); err == nil {
		testingT.Fatal("executable key document accepted")
	}
}

type sessionProviderStub struct {
	token   string
	cookies []*http.Cookie
}

func (stub sessionProviderStub) RiskSession(ctx context.Context) (string, []*http.Cookie, error) {
	return stub.token, stub.cookies, nil
}

func (stub sessionProviderStub) RiskToken(ctx context.Context) (string, error) {
	return stub.token, nil
}

type tokenProviderStub struct {
	token string
}

func (stub tokenProviderStub) RiskToken(ctx context.Context) (string, error) {
	return stub.token, nil
}

func TestPasswordLoginSeedsHandshakeCookies(testingT *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		testingT.Fatal(err)
	}
	keyDocument := fmt.Sprintf(`var thspubkey={"v":"fixture-key","m":"%X","p":"%X"};`, privateKey.N, privateKey.E)
	jar, _ := cookiejar.New(nil)
	var sawCookies []string
	client := &http.Client{Jar: jar, Transport: passwordTransport(func(request *http.Request) (*http.Response, error) {
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
			sawCookies = append(sawCookies, request.Header.Get("Cookie"))
			payload = `{"errorcode":0}`
			response.Header.Set("Set-Cookie", "ticket=fixture-session; Domain=.10jqka.com.cn; Path=/; Secure; HttpOnly")
		case "/mobileapi/hotspot_focus/chart_config/v1/query_config":
			payload = `{"status_code":0,"data":{"chart_list":[]}}`
		default:
			testingT.Fatalf("unexpected login request path %s", request.URL.Path)
		}
		response.Body = io.NopCloser(strings.NewReader(payload))
		return response, nil
	})}
	provider := sessionProviderStub{token: "fixture-token", cookies: []*http.Cookie{
		{Name: "v", Value: "fixture-token", Domain: ".10jqka.com.cn", Path: "/", Secure: true},
		{Name: "u_did", Value: "fixture-device", Domain: ".10jqka.com.cn", Path: "/", Secure: true, HttpOnly: true},
		{Name: "outsider", Value: "dropped", Domain: ".example.com", Path: "/"},
	}}
	if err := PasswordLoginWithRiskProvider(passwordFixtureCredentials(testingT), provider)(context.Background(), client); err != nil {
		testingT.Fatal(err)
	}
	if len(sawCookies) == 0 {
		testingT.Fatal("login was not submitted")
	}
	for _, header := range sawCookies {
		if !strings.Contains(header, "u_did=fixture-device") {
			testingT.Fatalf("handshake cookie was not seeded: %s", header)
		}
		if strings.Contains(header, "outsider=") {
			testingT.Fatalf("foreign cookie leaked into the jar: %s", header)
		}
		if !strings.Contains(header, "v=fixture-token") {
			testingT.Fatalf("risk token cookie missing: %s", header)
		}
	}
}

func TestPasswordLoginTokenOnlyProviderSeedsRiskCookie(testingT *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		testingT.Fatal(err)
	}
	keyDocument := fmt.Sprintf(`var thspubkey={"v":"fixture-key","m":"%X","p":"%X"};`, privateKey.N, privateKey.E)
	jar, _ := cookiejar.New(nil)
	var sawCookie string
	client := &http.Client{Jar: jar, Transport: passwordTransport(func(request *http.Request) (*http.Response, error) {
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
			sawCookie = request.Header.Get("Cookie")
			payload = `{"errorcode":0}`
			response.Header.Set("Set-Cookie", "ticket=fixture-session; Domain=.10jqka.com.cn; Path=/; Secure; HttpOnly")
		case "/mobileapi/hotspot_focus/chart_config/v1/query_config":
			payload = `{"status_code":0,"data":{"chart_list":[]}}`
		default:
			testingT.Fatalf("unexpected login request path %s", request.URL.Path)
		}
		response.Body = io.NopCloser(strings.NewReader(payload))
		return response, nil
	})}
	if err := PasswordLoginWithRiskProvider(passwordFixtureCredentials(testingT), tokenProviderStub{token: "fixture-token"})(context.Background(), client); err != nil {
		testingT.Fatal(err)
	}
	if !strings.Contains(sawCookie, "v=fixture-token") {
		testingT.Fatalf("risk token cookie was not injected: %s", sawCookie)
	}
}
