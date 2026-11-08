package thsheadless

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("headless browser unavailable")

const (
	riskTokenPollInterval    = 250 * time.Millisecond
	handshakePollInterval    = 750 * time.Millisecond
	handshakeStabilityWindow = 20 * time.Second
	handshakeMinimumWait     = 10 * time.Second
	maxHandshakeCookies      = 64
)

var errHandshakeTimeout = fmt.Errorf("%w: ths handshake deadline exceeded", ErrUnavailable)

type Options struct {
	Executable string
	ChromeArgs []string
	Timeout    time.Duration
}

type Client struct {
	options Options
}

type cdpSession interface {
	command(context.Context, string, map[string]any) (json.RawMessage, error)
	evaluate(context.Context, string) (string, error)
}

func (client *Client) RiskToken(ctx context.Context) (string, error) {
	_, cookie, err := client.Cookie(ctx, "https://upass.10jqka.com.cn/login")
	if err != nil {
		return "", err
	}
	return cookie.Value, nil
}

// RiskSession performs one login-page visit and returns the risk token together
// with every 10jqka handshake cookie captured in the same browser session. The
// official login rejects protocol submissions that carry only the v cookie, so
// callers must seed the whole set.
func (client *Client) RiskSession(ctx context.Context) (string, []*http.Cookie, error) {
	_, cookie, cookies, err := client.visit(ctx, "https://upass.10jqka.com.cn/login")
	if err != nil {
		return "", nil, err
	}
	return cookie.Value, cookies, nil
}

func New(options Options) (*Client, error) {
	if options.Executable == "" {
		return nil, fmt.Errorf("%w: executable is required", ErrUnavailable)
	}
	if options.Timeout <= 0 {
		options.Timeout = 30 * time.Second
	}
	info, err := os.Stat(options.Executable)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("%w: executable is not a regular file", ErrUnavailable)
	}
	return &Client{options: options}, nil
}

func (client *Client) Cookie(ctx context.Context, pageURL string) (string, *http.Cookie, error) {
	header, cookie, _, err := client.visit(ctx, pageURL)
	return header, cookie, err
}

func (client *Client) visit(ctx context.Context, pageURL string) (string, *http.Cookie, []*http.Cookie, error) {
	if client == nil {
		return "", nil, nil, ErrUnavailable
	}
	profile, err := os.MkdirTemp("", "shadowflow-ths-headless-")
	if err != nil {
		return "", nil, nil, fmt.Errorf("%w: create profile", ErrUnavailable)
	}
	defer os.RemoveAll(profile)
	if chmodError := os.Chmod(profile, 0o700); chmodError != nil {
		return "", nil, nil, fmt.Errorf("%w: secure profile", ErrUnavailable)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, fmt.Errorf("%w: reserve debug port", ErrUnavailable)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	args := []string{"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--user-data-dir=" + profile, "--remote-debugging-address=127.0.0.1", fmt.Sprintf("--remote-debugging-port=%d", port), "--remote-allow-origins=*", "--window-size=1280,900"}
	args = append(args, client.options.ChromeArgs...)
	args = append(args, "about:blank")
	command := exec.CommandContext(ctx, client.options.Executable, args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return "", nil, nil, fmt.Errorf("%w: start browser", ErrUnavailable)
	}
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForEndpoint(ctx, endpoint); err != nil {
		return "", nil, nil, err
	}
	tab, err := createTab(ctx, endpoint, pageURL)
	if err != nil {
		return "", nil, nil, err
	}
	cdp, err := dial(ctx, tab.WebSocketDebuggerURL)
	if err != nil {
		return "", nil, nil, err
	}
	defer cdp.close()
	cookie, cookies, waitError := waitForRiskSession(ctx, cdp, client.options.Timeout,
		handshakeStabilityWindow, riskTokenPollInterval, handshakePollInterval, handshakeMinimumWait)
	if waitError != nil {
		return "", nil, nil, waitError
	}
	cookie.Domain = ".10jqka.com.cn"
	cookie.Path = "/"
	cookie.Secure = true
	header := serializeCookie(cookie)
	return header, cookie, cookies, nil
}

type cdpCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Secure   bool    `json:"secure"`
	HTTPOnly bool    `json:"httpOnly"`
	Expires  float64 `json:"expires"`
}

func waitForRiskSession(ctx context.Context, session cdpSession, tokenTimeout, stabilityTimeout, tokenInterval, cookieInterval, minimumWait time.Duration) (*http.Cookie, []*http.Cookie, error) {
	if tokenError := waitForRiskToken(ctx, session, tokenTimeout, tokenInterval); tokenError != nil {
		return nil, nil, tokenError
	}
	return waitForStableRiskCookies(ctx, session, stabilityTimeout, cookieInterval, minimumWait)
}

func waitForRiskToken(ctx context.Context, session cdpSession, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if contextError := ctx.Err(); contextError != nil {
			return contextError
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%w: ths risk cookie was not generated", ErrUnavailable)
		}
		cookieValue, evaluateError := session.evaluate(ctx, "document.cookie")
		if evaluateError != nil {
			if contextError := ctx.Err(); contextError != nil {
				return contextError
			}
			return fmt.Errorf("%w: inspect browser cookies: %v", ErrUnavailable, evaluateError)
		}
		if contextError := ctx.Err(); contextError != nil {
			return contextError
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%w: ths risk cookie was not generated", ErrUnavailable)
		}
		if parsedCookie := parseCookieHeader(cookieValue); parsedCookie != nil && strings.TrimSpace(parsedCookie.Value) != "" {
			return nil
		}
		if waitError := waitForNextPoll(ctx, deadline, interval); waitError != nil {
			return waitError
		}
	}
}

func waitForStableRiskCookies(ctx context.Context, session cdpSession, timeout, interval, minimumWait time.Duration) (*http.Cookie, []*http.Cookie, error) {
	deadline := time.Now().Add(timeout)
	var previous []*http.Cookie
	var firstValidAt time.Time
	stableStreak := 0
	for {
		if contextError := ctx.Err(); contextError != nil {
			return nil, nil, contextError
		}
		if !time.Now().Before(deadline) {
			return nil, nil, fmt.Errorf("%w: ths handshake cookies did not stabilize", ErrUnavailable)
		}
		cookies, captureError := captureCookies(ctx, session)
		if captureError != nil {
			return nil, nil, captureError
		}
		if contextError := ctx.Err(); contextError != nil {
			return nil, nil, contextError
		}
		if !time.Now().Before(deadline) {
			return nil, nil, fmt.Errorf("%w: ths handshake cookies did not stabilize", ErrUnavailable)
		}
		observedAt := time.Now()
		riskCookie, riskValid := currentRiskCookie(cookies, observedAt)
		if riskValid && firstValidAt.IsZero() {
			firstValidAt = observedAt
		}
		if riskValid && len(previous) > 0 && sameCookieSet(previous, cookies) {
			stableStreak++
			if stableStreak >= 2 && observedAt.Sub(firstValidAt) >= minimumWait {
				return riskCookie, cookies, nil
			}
		} else {
			stableStreak = 0
		}
		if riskValid {
			previous = cookies
		} else {
			previous = nil
			firstValidAt = time.Time{}
		}
		if waitError := waitForNextPoll(ctx, deadline, interval); waitError != nil {
			return nil, nil, waitError
		}
	}
}

func waitForNextPoll(ctx context.Context, deadline time.Time, interval time.Duration) error {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return errHandshakeTimeout
	}
	wait := interval
	if wait <= 0 || wait > remaining {
		wait = remaining
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func captureCookies(ctx context.Context, session cdpSession) ([]*http.Cookie, error) {
	payload, commandError := session.command(ctx, "Storage.getCookies", nil)
	if commandError != nil {
		return nil, fmt.Errorf("%w: capture handshake cookies: %v", ErrUnavailable, commandError)
	}
	var result struct {
		Cookies []cdpCookie `json:"cookies"`
	}
	if decodeError := json.Unmarshal(payload, &result); decodeError != nil {
		return nil, fmt.Errorf("%w: decode handshake cookies", ErrUnavailable)
	}
	cookies := make([]*http.Cookie, 0, len(result.Cookies))
	for _, item := range result.Cookies {
		if item.Name == "" || len(item.Name) > 128 || len(item.Value) > 4096 {
			continue
		}
		domain := strings.ToLower(item.Domain)
		host := strings.TrimPrefix(domain, ".")
		if host != "10jqka.com.cn" && !strings.HasSuffix(host, ".10jqka.com.cn") {
			continue
		}
		path := item.Path
		if path == "" {
			path = "/"
		}
		parsed := &http.Cookie{
			Name: item.Name, Value: item.Value, Domain: domain, Path: path,
			Secure: item.Secure, HttpOnly: item.HTTPOnly,
		}
		if item.Expires > 0 {
			parsed.Expires = time.Unix(int64(item.Expires), 0)
		}
		cookies = append(cookies, parsed)
	}
	if len(cookies) > maxHandshakeCookies {
		return nil, fmt.Errorf("%w: excessive handshake cookie set", ErrUnavailable)
	}
	return cookies, nil
}

func currentRiskCookie(cookies []*http.Cookie, now time.Time) (*http.Cookie, bool) {
	var riskCookie *http.Cookie
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name != "v" || strings.TrimSpace(cookie.Value) == "" {
			continue
		}
		if !cookie.Expires.IsZero() && !cookie.Expires.After(now) {
			continue
		}
		if cookie.Valid() != nil {
			continue
		}
		if riskCookie != nil && riskCookie.Value != cookie.Value {
			return nil, false
		}
		riskCookie = cookie
	}
	return riskCookie, riskCookie != nil
}

func sameCookieSet(first, second []*http.Cookie) bool {
	if len(first) == 0 || len(second) == 0 || len(first) != len(second) {
		return false
	}
	signature := func(cookies []*http.Cookie) map[string]string {
		out := make(map[string]string, len(cookies))
		for _, cookie := range cookies {
			out[cookie.Name+"|"+cookie.Domain+"|"+cookie.Path] = cookie.Value
		}
		return out
	}
	firstMap, secondMap := signature(first), signature(second)
	for key, value := range firstMap {
		if secondMap[key] != value {
			return false
		}
	}
	return true
}

func parseCookieHeader(header string) *http.Cookie {
	for _, item := range strings.Split(header, ";") {
		name, value, found := strings.Cut(strings.TrimSpace(item), "=")
		if !found || name != "v" {
			continue
		}
		return &http.Cookie{Name: name, Value: value}
	}
	return nil
}

func serializeCookie(cookie *http.Cookie) string {
	if cookie == nil {
		return ""
	}
	return cookie.Name + "=" + cookie.Value
}

func waitForEndpoint(ctx context.Context, endpoint string) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/json/version", nil)
		if err == nil {
			response, requestErr := http.DefaultClient.Do(request)
			if requestErr == nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("%w: browser endpoint did not start", ErrUnavailable)
}

type tabInfo struct {
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func createTab(ctx context.Context, endpoint, pageURL string) (tabInfo, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint+"/json/new?"+url.QueryEscape(pageURL), nil)
	if err != nil {
		return tabInfo{}, fmt.Errorf("%w: invalid tab request", ErrUnavailable)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return tabInfo{}, fmt.Errorf("%w: create tab", ErrUnavailable)
	}
	defer response.Body.Close()
	var tab tabInfo
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&tab); err != nil || tab.WebSocketDebuggerURL == "" {
		return tabInfo{}, fmt.Errorf("%w: invalid tab response", ErrUnavailable)
	}
	return tab, nil
}

type cdpClient struct {
	connection net.Conn
	reader     *bufio.Reader
	mutex      sync.Mutex
	nextID     int64
}

func dial(ctx context.Context, endpoint string) (*cdpClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "ws" {
		return nil, fmt.Errorf("%w: invalid debugger endpoint", ErrUnavailable)
	}
	address := parsed.Host
	if !strings.Contains(address, ":") {
		address += ":80"
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("%w: connect debugger", ErrUnavailable)
	}
	key := base64.StdEncoding.EncodeToString([]byte("shadowflow-cdp"))
	request := "GET " + parsed.RequestURI() + " HTTP/1.1\r\nHost: " + parsed.Host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(connection, request); err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("%w: debugger handshake", ErrUnavailable)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil || response.StatusCode != http.StatusSwitchingProtocols {
		_ = connection.Close()
		return nil, fmt.Errorf("%w: debugger upgrade rejected", ErrUnavailable)
	}
	_ = response.Body.Close()
	return &cdpClient{connection: connection, reader: reader}, nil
}

func (client *cdpClient) close() {
	if client != nil && client.connection != nil {
		_ = client.connection.Close()
	}
}

func (client *cdpClient) evaluate(ctx context.Context, expression string) (string, error) {
	payload, err := client.command(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true})
	if err != nil {
		return "", err
	}
	var result struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(payload, &result) != nil {
		return "", fmt.Errorf("%w: debugger response", ErrUnavailable)
	}
	return result.Result.Value, nil
}

func (client *cdpClient) command(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	client.nextID++
	id := client.nextID
	payload := map[string]any{"id": id, "method": method, "params": params}
	body, _ := json.Marshal(payload)
	if err := client.writeText(body); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = client.connection.SetReadDeadline(deadline)
	for {
		frame, err := client.readFrame()
		if err != nil {
			return nil, fmt.Errorf("%w: debugger read", ErrUnavailable)
		}
		var response struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(frame, &response) != nil || response.ID != id {
			continue
		}
		return response.Result, nil
	}
}

func (client *cdpClient) writeText(payload []byte) error {
	return client.writeFrame(1, payload)
}

func (client *cdpClient) writeFrame(opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, 0x80|byte(length))
	case length <= 65535:
		header = append(header, 0x80|126, byte(length>>8), byte(length))
	default:
		header = append(header, 0x80|127, 0, 0, 0, 0, byte(length>>24), byte(length>>16), byte(length>>8), byte(length))
	}
	mask := []byte{1, 2, 3, 4}
	header = append(header, mask...)
	masked := make([]byte, len(payload))
	for index, value := range payload {
		masked[index] = value ^ mask[index%4]
	}
	if _, err := client.connection.Write(append(header, masked...)); err != nil {
		return err
	}
	return nil
}

func (client *cdpClient) readFrame() ([]byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(client.reader, header); err != nil {
		return nil, err
	}
	length := int(header[1] & 0x7f)
	if length == 126 {
		extended := make([]byte, 2)
		if _, err := io.ReadFull(client.reader, extended); err != nil {
			return nil, err
		}
		length = int(binary.BigEndian.Uint16(extended))
	} else if length == 127 {
		extended := make([]byte, 8)
		if _, err := io.ReadFull(client.reader, extended); err != nil {
			return nil, err
		}
		length = int(binary.BigEndian.Uint64(extended))
	}
	if length < 0 || length > 16<<20 {
		return nil, fmt.Errorf("%w: invalid debugger frame size", ErrUnavailable)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(client.reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}
