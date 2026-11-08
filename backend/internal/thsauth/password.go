package thsauth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const LoginURL = "https://upass.10jqka.com.cn/login"

var publicKeyAssignment = regexp.MustCompile(`(?s)^\s*var\s+thspubkey\s*=\s*(\{[^;]+\})\s*;?\s*$`)
var phoneUsername = regexp.MustCompile(`^1\d{10}$`)

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func LoadCredentials(path string) (Credentials, error) {
	if path == "" {
		return Credentials{}, ErrUnconfigured
	}
	if !filepath.IsAbs(path) {
		return Credentials{}, fmt.Errorf("%w: credential path must be absolute", ErrCredentials)
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 || parent.Mode().Perm()&0o077 != 0 {
		return Credentials{}, fmt.Errorf("%w: credential directory must be private", ErrCredentials)
	}
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, ErrUnconfigured
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("%w: cannot open credential file", ErrCredentials)
	}
	file := os.NewFile(uintptr(descriptor), "ths-credentials")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 8192 {
		return Credentials{}, fmt.Errorf("%w: credential file must be private and bounded", ErrCredentials)
	}
	decoder := json.NewDecoder(io.LimitReader(file, 8193))
	decoder.DisallowUnknownFields()
	var credentials Credentials
	if err := decoder.Decode(&credentials); err != nil {
		return Credentials{}, fmt.Errorf("%w: invalid credential file", ErrCredentials)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Credentials{}, fmt.Errorf("%w: invalid credential file", ErrCredentials)
	}
	credentials.Username = strings.TrimSpace(credentials.Username)
	if err := validateCredentials(credentials); err != nil {
		return Credentials{}, err
	}
	return credentials, nil
}

func validateCredentials(credentials Credentials) error {
	if credentials.Username == "" || credentials.Password == "" || len(credentials.Username) > 100 || len(credentials.Password) > 256 {
		return fmt.Errorf("%w: empty or oversized credentials", ErrCredentials)
	}
	if phoneUsername.MatchString(credentials.Username) || strings.Contains(credentials.Username, "@") {
		return fmt.Errorf("%w: official password login requires a username; phone and email use the interactive login", ErrCredentials)
	}
	return nil
}

type loginPublicKey struct {
	Version string `json:"v"`
	Modulus string `json:"m"`
	Power   string `json:"p"`
	Key     *rsa.PublicKey
}

type saltResponse struct {
	Code *int   `json:"errorcode"`
	DSK  string `json:"dsk"`
	DSV  string `json:"dsv"`
	SSV  string `json:"ssv"`
}

type RiskTokenProvider interface {
	RiskToken(context.Context) (string, error)
}

// RiskSessionProvider supplies the risk token together with the login-page
// handshake cookies captured in the same browser session. The official login
// rejects protocol submissions that carry only the token cookie, so session
// providers are preferred over token-only providers.
type RiskSessionProvider interface {
	RiskSession(ctx context.Context) (string, []*http.Cookie, error)
}

func PasswordLoginWithRiskProvider(credentialsFile string, provider RiskTokenProvider) LoginFunc {
	return PasswordLoginWithRiskProviderSource(FileCredentials(credentialsFile), provider)
}

// PasswordLoginWithRiskProviderSource builds the protocol login from an explicit
// credential source, so deployments can feed credentials from the environment
// instead of a mounted file.
func PasswordLoginWithRiskProviderSource(source CredentialSource, provider RiskTokenProvider) LoginFunc {
	return func(ctx context.Context, client *http.Client) error {
		credentials, err := source.Credentials(ctx)
		if err != nil {
			return err
		}
		if client == nil || client.Jar == nil {
			return fmt.Errorf("%w: login requires a cookie jar", ErrUnconfigured)
		}
		if provider == nil {
			return fmt.Errorf("%w: risk token provider is required", ErrUnconfigured)
		}
		safeClient := *client
		safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		var token string
		var handshake []*http.Cookie
		if session, ok := provider.(RiskSessionProvider); ok {
			sessionToken, cookies, sessionErr := session.RiskSession(ctx)
			if sessionErr != nil || sessionToken == "" {
				return loginFailure(ErrManualLogin, "risk_token", "headless_browser_failed", 0, nil)
			}
			token = sessionToken
			handshake = cookies
		} else {
			tokenOnly, tokenErr := provider.RiskToken(ctx)
			if tokenErr != nil || tokenOnly == "" {
				return loginFailure(ErrManualLogin, "risk_token", "headless_browser_failed", 0, nil)
			}
			token = tokenOnly
		}
		if err := seedHandshakeCookies(safeClient.Jar, handshake, token); err != nil {
			return loginFailure(err, "risk_token", "handshake_cookies_invalid", 0, nil)
		}
		safeClient.Transport = riskHeaderTransport{base: safeClient.Transport, token: token}
		return passwordLogin(ctx, &safeClient, credentials)
	}
}

type riskHeaderTransport struct {
	base  http.RoundTripper
	token string
}

func (transport riskHeaderTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header.Set("hexin-v", transport.token)
	base := transport.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}

func PasswordLogin(credentialsFile string) LoginFunc {
	return PasswordLoginWithSource(FileCredentials(credentialsFile))
}

// PasswordLoginWithSource builds the protocol login from an explicit credential
// source without a risk-token provider (no headless handshake; the caller must
// already carry the risk cookies).
func PasswordLoginWithSource(source CredentialSource) LoginFunc {
	return func(ctx context.Context, client *http.Client) error {
		credentials, err := source.Credentials(ctx)
		if err != nil {
			return err
		}
		if client == nil || client.Jar == nil {
			return fmt.Errorf("%w: login requires a cookie jar", ErrUnconfigured)
		}
		safeClient := *client
		safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return passwordLogin(ctx, &safeClient, credentials)
	}
}

func passwordLogin(ctx context.Context, safeClient *http.Client, credentials Credentials) error {
	keyBody, err := passwordRequest(ctx, safeClient, "https://upass.10jqka.com.cn/pubkey/default.js", nil)
	if err != nil {
		return err
	}
	key, err := parseLoginPublicKey(keyBody)
	if err != nil {
		return loginFailure(err, "public_key", "unexpected_response", http.StatusOK, nil)
	}
	encryptedName, err := key.encrypt(credentials.Username)
	if err != nil {
		return err
	}
	nonce, err := loginNonce()
	if err != nil {
		return err
	}
	passwordDigest := md5.Sum([]byte(credentials.Password))
	digest := hex.EncodeToString(passwordDigest[:])
	encryptedDigest, err := key.encrypt(digest)
	if err != nil {
		return err
	}
	var solution *captchaSolution
	saltRetried := false
	for challengeRound := 0; challengeRound < 3; challengeRound++ {
		salt, saltError := fetchPasswordSalt(ctx, safeClient, encryptedName, key.Version, nonce)
		if saltError != nil {
			return saltError
		}
		submission := passwordSubmit{
			encryptedName: encryptedName,
			encryptedPass: encryptedDigest,
			digest:        digest,
			nonce:         nonce,
			key:           key,
			salt:          salt,
			solution:      solution,
		}
		replyCode, replyDSK, replyDSV, submitError := submitPassword(ctx, safeClient, submission)
		if submitError != nil {
			return submitError
		}
		if replyCode == 0 {
			return verifyPasswordSession(ctx, safeClient)
		}
		if replyCode == -11032 {
			return loginFailure(passwordError(replyCode), "password_submit", passwordFailureKind(replyCode), http.StatusOK, &replyCode)
		}
		if isCaptchaBusinessCode(replyCode) {
			if solution != nil {
				return loginFailure(passwordError(replyCode), "password_submit", passwordFailureKind(replyCode), http.StatusOK, &replyCode)
			}
			solved, captchaError := solveCaptcha(ctx, safeClient)
			if captchaError != nil {
				return loginFailure(captchaError, "slider_captcha", "captcha_failed", http.StatusOK, &replyCode)
			}
			solution = &solved
			saltRetried = false
			continue
		}
		if replyDSK != "" && replyDSV != "" && !saltRetried {
			saltRetried = true
			submission.salt = saltResponse{Code: replyCodePtr(0), DSK: replyDSK, DSV: replyDSV}
			submission.attempt = 2
			replyCode, _, _, submitError = submitPassword(ctx, safeClient, submission)
			if submitError != nil {
				return submitError
			}
			if replyCode == 0 {
				return verifyPasswordSession(ctx, safeClient)
			}
			if isCaptchaBusinessCode(replyCode) && solution == nil {
				solved, captchaError := solveCaptcha(ctx, safeClient)
				if captchaError != nil {
					return loginFailure(captchaError, "slider_captcha", "captcha_failed", http.StatusOK, &replyCode)
				}
				solution = &solved
				saltRetried = false
				continue
			}
		}
		return loginFailure(passwordError(replyCode), "password_submit", passwordFailureKind(replyCode), http.StatusOK, &replyCode)
	}
	return loginFailure(ErrChallenge, "password_submit", "slider_required", http.StatusOK, nil)
}

func replyCodePtr(value int) *int {
	return &value
}

// seedHandshakeCookies loads the browser handshake cookie set into the jar and
// guarantees the v risk cookie matches the supplied token.
func seedHandshakeCookies(jar http.CookieJar, cookies []*http.Cookie, token string) error {
	if len(cookies) > 64 {
		return fmt.Errorf("%w: excessive handshake cookie set", ErrManualLogin)
	}
	tokenSeeded := false
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" || len(cookie.Name) > 128 || len(cookie.Value) > 4096 {
			continue
		}
		domain := strings.ToLower(cookie.Domain)
		host := strings.TrimPrefix(domain, ".")
		if host != "10jqka.com.cn" && !strings.HasSuffix(host, ".10jqka.com.cn") {
			continue
		}
		path := cookie.Path
		if path == "" || !strings.HasPrefix(path, "/") {
			path = "/"
		}
		jar.SetCookies(&url.URL{Scheme: "https", Host: host, Path: path}, []*http.Cookie{{
			Name: cookie.Name, Value: cookie.Value, Domain: domain, Path: path,
			Secure: cookie.Secure, HttpOnly: cookie.HttpOnly,
		}})
		if cookie.Name == "v" && cookie.Value == token {
			tokenSeeded = true
		}
	}
	if !tokenSeeded && token != "" {
		jar.SetCookies(&url.URL{Scheme: "https", Host: "upass.10jqka.com.cn", Path: "/"}, []*http.Cookie{{
			Name: "v", Value: token, Domain: ".10jqka.com.cn", Path: "/", Secure: true,
		}})
	}
	return nil
}

// loginNonce returns a 16-character lowercase alphanumeric nonce matching the
// official web frontend's crnd format (e.g. "wvdtvix62lq1fqig").
func loginNonce() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", ErrUnavailable
	}
	nonce := make([]byte, 16)
	for index, value := range raw {
		nonce[index] = alphabet[int(value)%len(alphabet)]
	}
	return string(nonce), nil
}

type passwordSubmit struct {
	encryptedName string
	encryptedPass string
	digest        string
	nonce         string
	key           loginPublicKey
	salt          saltResponse
	solution      *captchaSolution
	attempt       int
}

func fetchPasswordSalt(ctx context.Context, client *http.Client, encryptedName, version, nonce string) (saltResponse, error) {
	body, err := passwordRequest(ctx, client, "https://upass.10jqka.com.cn/user/getGS", url.Values{
		"uname": {encryptedName}, "rsa_version": {version}, "crnd": {nonce},
	})
	if err != nil {
		return saltResponse{}, err
	}
	var salt saltResponse
	if err := json.Unmarshal(body, &salt); err != nil || salt.Code == nil {
		return saltResponse{}, loginFailure(ErrUnavailable, "password_salt", "unexpected_response", http.StatusOK, nil)
	}
	if *salt.Code != 0 {
		return saltResponse{}, loginFailure(passwordError(*salt.Code), "password_salt", passwordFailureKind(*salt.Code), http.StatusOK, salt.Code)
	}
	return salt, nil
}

func submitPassword(ctx context.Context, client *http.Client, submission passwordSubmit) (int, string, string, error) {
	attempt := submission.attempt
	if attempt == 0 {
		attempt = 1
	}
	encodedSalt, err := saltedPassword(submission.digest, submission.nonce, submission.salt, attempt)
	if err != nil {
		return 0, "", "", loginFailure(err, "password_salt", "unexpected_response", http.StatusOK, nil)
	}
	encryptedSalt, err := submission.key.encrypt(encodedSalt)
	if err != nil {
		return 0, "", "", err
	}
	values := url.Values{
		"uname": {submission.encryptedName}, "passwd": {submission.encryptedPass}, "passwdSalt": {encryptedSalt},
		"rsa_version": {submission.key.Version}, "saltLoginTimes": {strconv.Itoa(attempt)}, "dsk": {submission.salt.DSK},
		"crnd": {submission.nonce}, "longLogin": {"0"}, "source": {"pc_web"}, "request_type": {"login"},
		"captcha_type": {"4"}, "ttype": {"WEB"}, "sdtis": {"C22"},
		"upwd_score": {"50"},
		"timestamp":  {strconv.FormatInt(time.Now().Unix(), 10)},
	}
	if submission.solution != nil {
		values.Set("captcha_phrase", submission.solution.Phrase)
		values.Set("captcha_ticket", submission.solution.Ticket)
		values.Set("captcha_signature", submission.solution.Signature)
	}
	body, err := passwordRequest(ctx, client, "https://upass.10jqka.com.cn/login/dologinreturnjson2", values)
	if err != nil {
		return 0, "", "", err
	}
	var reply saltResponse
	if err := json.Unmarshal(body, &reply); err != nil || reply.Code == nil {
		return 0, "", "", loginFailure(ErrChallenge, "password_submit", "unexpected_response", http.StatusOK, nil)
	}
	return *reply.Code, reply.DSK, reply.DSV, nil
}

func isCaptchaBusinessCode(code int) bool {
	return code == -11400 || code == -10507
}

func passwordRequest(ctx context.Context, client *http.Client, target string, values url.Values) ([]byte, error) {
	stage := passwordStage(target)
	gatewayRetry := false
	for {
		method := http.MethodGet
		var body io.Reader
		if values != nil {
			method, body = http.MethodPost, strings.NewReader(values.Encode())
		}
		request, err := http.NewRequestWithContext(ctx, method, target, body)
		if err != nil {
			return nil, loginFailure(ErrUnavailable, stage, "invalid_request", 0, nil)
		}
		request.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36")
		request.Header.Set("Referer", LoginURL)
		request.Header.Set("Accept", "application/json, text/javascript, text/html, */*")
		if values != nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Origin", "https://upass.10jqka.com.cn")
		}
		response, err := client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, loginFailure(ErrUnavailable, stage, "network_error", 0, nil)
		}
		if response.StatusCode == http.StatusUnauthorized && values == nil && !gatewayRetry {
			gatewayRetry = true
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			continue
		}
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests {
			_ = response.Body.Close()
			return nil, loginFailure(ErrChallenge, stage, "access_challenge", response.StatusCode, nil)
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			return nil, loginFailure(ErrUnavailable, stage, "http_error", response.StatusCode, nil)
		}
		payload, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
		_ = response.Body.Close()
		if err != nil || len(payload) > 256<<10 || len(payload) == 0 {
			return nil, loginFailure(ErrUnavailable, stage, "incomplete_response", response.StatusCode, nil)
		}
		return payload, nil
	}
}

func parseLoginPublicKey(body []byte) (loginPublicKey, error) {
	match := publicKeyAssignment.FindSubmatch(body)
	if len(match) != 2 {
		return loginPublicKey{}, fmt.Errorf("%w: official key format changed", ErrChallenge)
	}
	var key loginPublicKey
	if err := json.Unmarshal(match[1], &key); err != nil || key.Version == "" || len(key.Version) > 64 {
		return loginPublicKey{}, fmt.Errorf("%w: invalid official key", ErrUnavailable)
	}
	modulus, valid := new(big.Int).SetString(key.Modulus, 16)
	power, err := strconv.ParseInt(key.Power, 16, 32)
	if !valid || err != nil || modulus.BitLen() < 1024 || modulus.BitLen() > 4096 || power < 3 || power%2 == 0 {
		return loginPublicKey{}, fmt.Errorf("%w: invalid official key parameters", ErrUnavailable)
	}
	key.Key = &rsa.PublicKey{N: modulus, E: int(power)}
	return key, nil
}

func (key loginPublicKey) encrypt(value string) (string, error) {
	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, key.Key, []byte(value))
	if err != nil {
		return "", fmt.Errorf("%w: credential encryption failed", ErrCredentials)
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func saltedPassword(digest, nonce string, salt saltResponse, attempt int) (string, error) {
	if salt.DSK == "" || salt.DSV == "" || len(salt.DSK) > 4096 || len(salt.DSV) > 4096 {
		return "", fmt.Errorf("%w: incomplete login salt", ErrUnavailable)
	}
	value := digest
	if attempt == 1 {
		if salt.SSV == "" || len(salt.SSV) > 8192 {
			return "", fmt.Errorf("%w: incomplete login salt", ErrUnavailable)
		}
		decoded, err := base64.StdEncoding.DecodeString(salt.SSV)
		if err != nil {
			return "", fmt.Errorf("%w: invalid login salt encoding", ErrUnavailable)
		}
		parts := strings.Split(string(xorBytes(decoded, []byte(sha256Hex(nonce+salt.DSK)))), "=")
		if len(parts) < 2 || parts[1] == "" {
			return "", fmt.Errorf("%w: invalid login salt contents", ErrUnavailable)
		}
		mac := hmac.New(sha256.New, []byte(parts[1]))
		mac.Write([]byte(digest))
		value = hex.EncodeToString(mac.Sum(nil))
	} else if attempt != 2 {
		return "", fmt.Errorf("%w: unsupported login salt step", ErrUnavailable)
	}
	return base64.StdEncoding.EncodeToString(xorBytes([]byte(value), []byte(sha256Hex(salt.DSV)))), nil
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func xorBytes(value, key []byte) []byte {
	result := make([]byte, len(value))
	for index, character := range value {
		result[index] = character ^ key[index%len(key)]
	}
	return result
}

func isInteractivePasswordCode(code int) bool {
	switch code {
	case -10507, -11400, -10501, -10510, -10528, -10527:
		return true
	default:
		return false
	}
}

func passwordError(code int) error {
	if code == -11032 {
		return fmt.Errorf("%w: official login code %d", ErrCredentials, code)
	}
	return fmt.Errorf("%w: official login code %d", ErrChallenge, code)
}

func loginFailure(cause error, stage, kind string, status int, code *int) error {
	failure := LoginFailure{Stage: stage, Kind: kind, HTTPStatus: status}
	if code != nil {
		copiedCode := *code
		failure.BusinessCode = &copiedCode
	}
	return &LoginError{Failure: failure, Cause: cause}
}

func passwordStage(target string) string {
	switch target {
	case LoginURL:
		return "login_page"
	case "https://upass.10jqka.com.cn/pubkey/default.js":
		return "public_key"
	case "https://upass.10jqka.com.cn/user/getGS":
		return "password_salt"
	case "https://upass.10jqka.com.cn/login/dologinreturnjson2":
		return "password_submit"
	case "https://data.10jqka.com.cn/mobileapi/hotspot_focus/chart_config/v1/query_config":
		return "verify_session"
	default:
		return "login_request"
	}
}

func passwordFailureKind(code int) string {
	switch code {
	case -10507, -11400:
		return "slider_required"
	case -10510:
		return "device_verification"
	case -10528, -10527:
		return "password_action_required"
	case -10501:
		return "account_refused"
	case -11032:
		return "credentials_rejected"
	default:
		return "unrecognized_business_code"
	}
}

func verifyPasswordSession(ctx context.Context, client *http.Client) error {
	body, err := passwordRequest(ctx, client, "https://data.10jqka.com.cn/mobileapi/hotspot_focus/chart_config/v1/query_config", nil)
	if err != nil {
		return err
	}
	var result struct {
		Code *int            `json:"status_code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &result) != nil || result.Code == nil || *result.Code != 0 || len(result.Data) == 0 || bytes.Equal(bytes.TrimSpace(result.Data), []byte("null")) {
		return loginFailure(ErrAuthExpired, "verify_session", "session_not_verified", http.StatusOK, result.Code)
	}
	return nil
}
