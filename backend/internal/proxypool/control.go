package proxypool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type validationScope struct {
	Mode   string `json:"mode"`
	Target string `json:"target,omitempty"`
}

var ErrControlPlane = errors.New("proxy control plane unavailable")

type controlError struct {
	Status int
	Code   string
}

func (failure *controlError) Error() string {
	return fmt.Sprintf("proxy control HTTP %d (%s)", failure.Status, failure.Code)
}

func (failure *controlError) temporary() bool {
	return failure.Status == http.StatusTooManyRequests || failure.Status >= http.StatusInternalServerError
}

type supplyError struct{ Reason string }

func (failure *supplyError) Error() string { return "proxy supply unavailable: " + failure.Reason }
func (failure *supplyError) Unwrap() error { return ErrNoProxy }

type prewarmJob struct {
	ID         string     `json:"job_id"`
	Kind       string     `json:"kind"`
	State      string     `json:"state"`
	Target     int        `json:"target"`
	Ready      int        `json:"ready"`
	Probed     int        `json:"probed"`
	Failed     int        `json:"failed"`
	StopReason string     `json:"stop_reason"`
	FinishedAt *time.Time `json:"finished_at"`
}

type acquireResponse struct {
	Requested        int            `json:"requested"`
	Delivered        int            `json:"delivered"`
	Missing          int            `json:"missing"`
	Reason           string         `json:"reason"`
	Items            []acquiredItem `json:"items"`
	SessionID        string         `json:"session_id"`
	SessionChanged   *bool          `json:"session_changed"`
	BindingVersion   int64          `json:"binding_version"`
	BindingExpiresAt time.Time      `json:"binding_expires_at"`
}

type acquiredItem struct {
	LeaseID    string `json:"lease_id"`
	SourceID   string `json:"source_id"`
	Connection struct {
		Transport string  `json:"transport"`
		Host      string  `json:"host"`
		Port      int     `json:"port"`
		Username  string  `json:"username"`
		Password  *string `json:"password"`
	} `json:"connection"`
	Validation struct {
		Mode        string    `json:"mode"`
		Target      string    `json:"target"`
		CompletedAt time.Time `json:"completed_at"`
		ValidUntil  time.Time `json:"valid_until"`
	} `json:"validation"`
	LeaseExpiresAt    time.Time  `json:"lease_expires_at"`
	ProviderExpiresAt *time.Time `json:"provider_expires_at"`
}

func scopeForURL(rawURL string) (validationScope, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return validationScope{}, errors.New("invalid upstream target URL")
	}
	if parsed.Scheme == "http" {
		return validationScope{Mode: "basic"}, nil
	}
	if parsed.Scheme != "https" {
		return validationScope{}, errors.New("upstream target must use http or https")
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	return validationScope{Mode: "tls", Target: net.JoinHostPort(strings.ToLower(parsed.Hostname()), port)}, nil
}

func normalizeControlURL(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("proxy pool URL must be an http(s) service base without credentials, query, or fragment")
	}
	if path := strings.TrimRight(parsed.Path, "/"); path != "" && path != "/api/v1" {
		return "", errors.New("proxy pool URL must be the service base or /api/v1, not a legacy proxy-list endpoint")
	}
	parsed.Path, parsed.RawPath = "/api/v1", ""
	return parsed.String(), nil
}

func safeIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 128 || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func safeReason(value string) string {
	if !safeIdentifier(value) {
		return "unspecified"
	}
	return value
}

func (manager *Manager) control(ctx context.Context, method, path string, body, result any, budget time.Duration, accepted ...int) (resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("%w: %w", ErrControlPlane, resultErr)
		}
	}()
	requestCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return errors.New("encode proxy control request")
		}
	}
	request, err := http.NewRequestWithContext(requestCtx, method, manager.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return errors.New("build proxy control request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+manager.options.Token)
	request.Header.Set("User-Agent", "shadowflow-proxy-client/2")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := manager.client.Do(request)
	if err != nil {
		if requestCtx.Err() != nil {
			return fmt.Errorf("proxy control request: %w", requestCtx.Err())
		}
		return errors.New("proxy control connection failed")
	}
	defer response.Body.Close()
	payload, err := readLimited(response.Body, 1<<20)
	if err != nil {
		return errors.New("read proxy control response")
	}
	validStatus := false
	for _, status := range accepted {
		validStatus = validStatus || response.StatusCode == status
	}
	if !validStatus {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(payload, &envelope)
		return &controlError{Status: response.StatusCode, Code: safeReason(envelope.Error.Code)}
	}
	if result != nil && json.Unmarshal(payload, result) != nil {
		return errors.New("invalid proxy control JSON response")
	}
	return nil
}

func (manager *Manager) releaseLease(ctx context.Context, leaseID string) error {
	if !safeIdentifier(leaseID) {
		return errors.New("invalid proxy lease identifier")
	}
	err := manager.control(ctx, http.MethodPost, "/leases/"+leaseID+"/release", struct{}{}, nil,
		manager.options.ControlTimeout, http.StatusOK, http.StatusNoContent, http.StatusNotFound)
	manager.recordCleanup(err)
	if err == nil {
		manager.mu.Lock()
		delete(manager.leases, leaseID)
		manager.mu.Unlock()
	}
	return err
}

func (manager *Manager) releaseSession(ctx context.Context, sessionID string) error {
	if !safeIdentifier(sessionID) {
		return errors.New("invalid proxy session identifier")
	}
	err := manager.control(ctx, http.MethodDelete, "/sessions/"+sessionID, nil, nil,
		manager.options.ControlTimeout, http.StatusOK, http.StatusNoContent, http.StatusNotFound)
	manager.recordCleanup(err)
	if err == nil {
		manager.mu.Lock()
		delete(manager.sessions, sessionID)
		manager.mu.Unlock()
	}
	return err
}

func durationSeconds(duration time.Duration) int64 {
	return max(1, int64((duration+time.Second-1)/time.Second))
}

func workerSessionID(prefix string, workerID int, scope validationScope) string {
	return prefix + "-" + strconv.Itoa(workerID) + "-" + fingerprint(scope.Mode, scope.Target)[:16]
}
