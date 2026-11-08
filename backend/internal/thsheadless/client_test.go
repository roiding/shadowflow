package thsheadless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeCDPSession struct {
	evaluateFunc func(context.Context, string) (string, error)
	commandFunc  func(context.Context, string, map[string]any) (json.RawMessage, error)
}

func (session *fakeCDPSession) evaluate(ctx context.Context, expression string) (string, error) {
	if session.evaluateFunc == nil {
		return "", errors.New("fixture evaluate is not configured")
	}
	return session.evaluateFunc(ctx, expression)
}

func (session *fakeCDPSession) command(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	if session.commandFunc == nil {
		return nil, errors.New("fixture command is not configured")
	}
	return session.commandFunc(ctx, method, params)
}

type fakeCookieResult struct {
	payload json.RawMessage
	err     error
}

func fakeCookiePayload(testingT *testing.T, cookies ...cdpCookie) fakeCookieResult {
	testingT.Helper()
	payload, marshalError := json.Marshal(struct {
		Cookies []cdpCookie `json:"cookies"`
	}{Cookies: cookies})
	if marshalError != nil {
		testingT.Fatal(marshalError)
	}
	return fakeCookieResult{payload: payload}
}

func fixedCommand(result fakeCookieResult) func(context.Context, string, map[string]any) (json.RawMessage, error) {
	return func(context.Context, string, map[string]any) (json.RawMessage, error) {
		return result.payload, result.err
	}
}

func fixedEvaluate(value string, resultError error) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) {
		return value, resultError
	}
}

func TestHeadlessCookieLive(testingT *testing.T) {
	if os.Getenv("SHADOWFLOW_THS_HEADLESS_LIVE") != "1" {
		testingT.Skip("set SHADOWFLOW_THS_HEADLESS_LIVE=1 to opt in to the real headless browser check")
	}
	executable := os.Getenv("SHADOWFLOW_THS_HEADLESS_BROWSER")
	if executable == "" {
		testingT.Skip("set SHADOWFLOW_THS_HEADLESS_BROWSER to run the local headless browser check")
	}
	client, err := New(Options{Executable: executable, Timeout: 20 * time.Second})
	if err != nil {
		testingT.Fatal(err)
	}
	header, cookie, err := client.Cookie(context.Background(), "https://upass.10jqka.com.cn/login")
	if err != nil {
		testingT.Fatal(err)
	}
	if cookie == nil || cookie.Name != "v" || cookie.Value == "" || header == "" {
		testingT.Fatal("browser did not return a valid risk cookie")
	}
	token, cookies, err := client.RiskSession(context.Background())
	if err != nil {
		testingT.Fatal(err)
	}
	if token == "" {
		testingT.Fatal("risk session token is empty")
	}
	names := make(map[string]bool, len(cookies))
	for _, item := range cookies {
		names[item.Name] = true
	}
	if !names["v"] || !names["PHPSESSID"] {
		testingT.Fatalf("handshake cookie set incomplete: %v", names)
	}
}

func TestWaitForRiskSessionRejectsCookieCaptureFailure(testingT *testing.T) {
	session := &fakeCDPSession{
		evaluateFunc: fixedEvaluate("v=fixture", nil),
		commandFunc:  fixedCommand(fakeCookieResult{err: errors.New("fixture command failure")}),
	}
	_, _, waitError := waitForRiskSession(context.Background(), session,
		50*time.Millisecond, 50*time.Millisecond, time.Millisecond, time.Millisecond)
	if !errors.Is(waitError, ErrUnavailable) {
		testingT.Fatalf("cookie capture failure was not returned: %v", waitError)
	}
}

func TestWaitForRiskSessionRejectsCookieEvaluationFailure(testingT *testing.T) {
	session := &fakeCDPSession{
		evaluateFunc: fixedEvaluate("", errors.New("fixture evaluate failure")),
		commandFunc:  fixedCommand(fakeCookiePayload(testingT)),
	}
	_, _, waitError := waitForRiskSession(context.Background(), session,
		50*time.Millisecond, 50*time.Millisecond, time.Millisecond, time.Millisecond)
	if !errors.Is(waitError, ErrUnavailable) {
		testingT.Fatalf("cookie evaluation failure was not returned: %v", waitError)
	}
}

func TestWaitForRiskSessionRejectsMalformedCookieJSON(testingT *testing.T) {
	session := &fakeCDPSession{
		evaluateFunc: fixedEvaluate("v=fixture", nil),
		commandFunc:  fixedCommand(fakeCookieResult{payload: json.RawMessage("{")}),
	}
	_, _, waitError := waitForRiskSession(context.Background(), session,
		50*time.Millisecond, 50*time.Millisecond, time.Millisecond, time.Millisecond)
	if !errors.Is(waitError, ErrUnavailable) {
		testingT.Fatalf("malformed cookie JSON was not returned as an error: %v", waitError)
	}
}

func TestWaitForRiskSessionRejectsEmptyCookieSet(testingT *testing.T) {
	session := &fakeCDPSession{
		evaluateFunc: fixedEvaluate("v=fixture", nil),
		commandFunc:  fixedCommand(fakeCookiePayload(testingT)),
	}
	_, _, waitError := waitForRiskSession(context.Background(), session,
		50*time.Millisecond, 50*time.Millisecond, time.Millisecond, time.Millisecond)
	if !errors.Is(waitError, ErrUnavailable) {
		testingT.Fatalf("empty cookie set was accepted: %v", waitError)
	}
}

func TestWaitForRiskSessionAcceptsStableCookiesAfterChange(testingT *testing.T) {
	initial := fakeCookiePayload(testingT,
		cdpCookie{Name: "v", Value: "fixture-risk", Domain: ".10jqka.com.cn", Path: "/", Secure: true},
		cdpCookie{Name: "u_did", Value: "fixture-device", Domain: ".10jqka.com.cn", Path: "/", HTTPOnly: true},
	)
	complete := fakeCookiePayload(testingT,
		cdpCookie{Name: "v", Value: "fixture-risk", Domain: ".10jqka.com.cn", Path: "/", Secure: true},
		cdpCookie{Name: "u_did", Value: "fixture-device", Domain: ".10jqka.com.cn", Path: "/", HTTPOnly: true},
		cdpCookie{Name: "PHPSESSID", Value: "fixture-session", Domain: ".10jqka.com.cn", Path: "/", Secure: true},
	)
	callCount := 0
	session := &fakeCDPSession{
		evaluateFunc: fixedEvaluate("v=fixture-risk", nil),
		commandFunc: func(context.Context, string, map[string]any) (json.RawMessage, error) {
			callCount++
			if callCount <= 2 {
				return initial.payload, nil
			}
			return complete.payload, nil
		},
	}
	riskCookie, cookies, waitError := waitForRiskSession(context.Background(), session,
		time.Second, time.Second, time.Millisecond, time.Millisecond)
	if waitError != nil {
		testingT.Fatal(waitError)
	}
	if riskCookie == nil || riskCookie.Value != "fixture-risk" {
		testingT.Fatalf("unexpected risk cookie: %+v", riskCookie)
	}
	if len(cookies) != 3 {
		testingT.Fatalf("stable set was not exported: %d cookies", len(cookies))
	}
	if callCount < 5 {
		testingT.Fatal("handshake returned before two unchanged polling intervals")
	}
}

func TestWaitForRiskSessionUsesRotatedRiskCookie(testingT *testing.T) {
	initial := fakeCookiePayload(testingT,
		cdpCookie{Name: "v", Value: "stale-risk", Domain: ".10jqka.com.cn", Path: "/", Secure: true},
	)
	rotated := fakeCookiePayload(testingT,
		cdpCookie{Name: "v", Value: "current-risk", Domain: ".10jqka.com.cn", Path: "/", Secure: true},
		cdpCookie{Name: "u_did", Value: "fixture-device", Domain: ".10jqka.com.cn", Path: "/", HTTPOnly: true},
	)
	callCount := 0
	session := &fakeCDPSession{
		evaluateFunc: fixedEvaluate("v=stale-risk", nil),
		commandFunc: func(context.Context, string, map[string]any) (json.RawMessage, error) {
			callCount++
			if callCount == 1 {
				return initial.payload, nil
			}
			return rotated.payload, nil
		},
	}
	riskCookie, cookies, waitError := waitForRiskSession(context.Background(), session,
		time.Second, time.Second, time.Millisecond, time.Millisecond)
	if waitError != nil {
		testingT.Fatal(waitError)
	}
	if riskCookie == nil || riskCookie.Value != "current-risk" {
		testingT.Fatalf("stale document cookie was returned: %+v", riskCookie)
	}
	foundCurrentRisk := false
	for _, cookie := range cookies {
		if cookie.Name == "v" && cookie.Value == riskCookie.Value {
			foundCurrentRisk = true
		}
	}
	if !foundCurrentRisk {
		testingT.Fatal("exported cookies do not contain the current risk cookie")
	}
}

func TestWaitForRiskSessionHonorsCancellation(testingT *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	session := &fakeCDPSession{
		evaluateFunc: fixedEvaluate("v=fixture", nil),
		commandFunc: func(context.Context, string, map[string]any) (json.RawMessage, error) {
			cancel()
			return fakeCookiePayload(testingT,
				cdpCookie{Name: "v", Value: "fixture", Domain: ".10jqka.com.cn", Path: "/"},
			).payload, nil
		},
	}
	_, _, waitError := waitForRiskSession(ctx, session,
		time.Second, time.Second, time.Millisecond, time.Millisecond)
	if !errors.Is(waitError, context.Canceled) {
		testingT.Fatalf("cancellation did not stop the wait: %v", waitError)
	}
}

func TestWaitForRiskSessionRejectsExpiredStabilityBudget(testingT *testing.T) {
	callCount := 0
	session := &fakeCDPSession{
		evaluateFunc: fixedEvaluate("v=fixture", nil),
		commandFunc: func(context.Context, string, map[string]any) (json.RawMessage, error) {
			callCount++
			riskValue := "first"
			if callCount%2 == 0 {
				riskValue = "second"
			}
			return fakeCookiePayload(testingT,
				cdpCookie{Name: "v", Value: riskValue, Domain: ".10jqka.com.cn", Path: "/"},
			).payload, nil
		},
	}
	_, _, waitError := waitForRiskSession(context.Background(), session,
		time.Second, 30*time.Millisecond, time.Millisecond, time.Millisecond)
	if !errors.Is(waitError, ErrUnavailable) {
		testingT.Fatalf("expired stability budget was accepted: %v", waitError)
	}
}

func TestCaptureCookiesPreservesDomainAndSizeLimits(testingT *testing.T) {
	session := &fakeCDPSession{
		commandFunc: fixedCommand(fakeCookiePayload(testingT,
			cdpCookie{Name: "v", Value: "fixture-risk", Domain: ".10jqka.com.cn", Path: "/"},
			cdpCookie{Name: "foreign", Value: "discard", Domain: ".example.com", Path: "/"},
			cdpCookie{Name: "oversized", Value: strings.Repeat("x", 4097), Domain: ".10jqka.com.cn", Path: "/"},
		)),
	}
	cookies, captureError := captureCookies(context.Background(), session)
	if captureError != nil {
		testingT.Fatal(captureError)
	}
	if len(cookies) != 1 || cookies[0].Name != "v" {
		testingT.Fatalf("domain or size filtering changed: %+v", cookies)
	}
}

func TestCaptureCookiesRejectsExcessiveCookieCount(testingT *testing.T) {
	manyCookies := make([]cdpCookie, maxHandshakeCookies+1)
	for index := range manyCookies {
		manyCookies[index] = cdpCookie{
			Name: fmt.Sprintf("cookie_%d", index), Value: "fixture", Domain: ".10jqka.com.cn", Path: "/",
		}
	}
	session := &fakeCDPSession{commandFunc: fixedCommand(fakeCookiePayload(testingT, manyCookies...))}
	_, captureError := captureCookies(context.Background(), session)
	if !errors.Is(captureError, ErrUnavailable) {
		testingT.Fatalf("excessive cookie set was accepted: %v", captureError)
	}
}

func TestSameCookieSetRejectsEmptySets(testingT *testing.T) {
	if sameCookieSet(nil, nil) || sameCookieSet([]*http.Cookie{}, []*http.Cookie{}) {
		testingT.Fatal("empty cookie sets were considered stable")
	}
}
