package thsauth

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const (
	StateReady        = "ready"
	StateUnconfigured = "unconfigured"
	StateExpired      = "expired"
	StateRefreshing   = "refreshing"
	StateActionNeeded = "action_required"
	StateCooldown     = "cooldown"
)

var (
	ErrAuthExpired  = errors.New("ths authentication expired")
	ErrCredentials  = errors.New("ths credentials rejected")
	ErrChallenge    = errors.New("ths interactive challenge required")
	ErrPermission   = errors.New("ths permission denied")
	ErrUnavailable  = errors.New("ths authentication unavailable")
	ErrCooldown     = errors.New("ths authentication cooldown")
	ErrUnconfigured = errors.New("ths authentication unconfigured")
	ErrManualLogin  = errors.New("ths headless login unavailable")
)

type LoginFunc func(context.Context, *http.Client) error

// SessionStore persists the login-state cookie set across processes so the
// session survives restarts without a mounted session file. Implementations
// must be safe for concurrent use.
type SessionStore interface {
	LoadSession(ctx context.Context) ([]*http.Cookie, bool, error)
	SaveSession(ctx context.Context, cookies []*http.Cookie) error
}

// CredentialSource resolves the login credentials at login time. File sources
// keep the hardened on-disk checks; environment sources resolve the process
// environment so compose-provided credentials apply without mounted files.
type CredentialSource interface {
	Credentials(ctx context.Context) (Credentials, error)
}

type LoginFailure struct {
	Stage        string `json:"stage"`
	Kind         string `json:"kind"`
	HTTPStatus   int    `json:"http_status,omitempty"`
	BusinessCode *int   `json:"business_code,omitempty"`
}

type LoginError struct {
	Failure LoginFailure
	Cause   error
}

func (failure *LoginError) Error() string {
	return "ths protocol login failed at " + failure.Failure.Stage + ": " + failure.Failure.Kind
}

func (failure *LoginError) Unwrap() error {
	return failure.Cause
}

type Options struct {
	// SessionStore takes precedence over SessionFile when both are set.
	Session        SessionStore
	SessionFile    string
	AutoLogin      bool
	Login          LoginFunc
	Transport      http.RoundTripper
	RequestTimeout time.Duration
	RefreshTimeout time.Duration
	Cooldown       time.Duration
}

type Status struct {
	State         string        `json:"state"`
	AutoLogin     bool          `json:"auto_login"`
	ExpiresAt     *time.Time    `json:"expires_at,omitempty"`
	LastRefreshAt *time.Time    `json:"last_refresh_at,omitempty"`
	RefreshCount  int           `json:"refresh_count"`
	RetryAt       *time.Time    `json:"retry_at,omitempty"`
	LastErrorCode string        `json:"last_error_code,omitempty"`
	LastFailure   *LoginFailure `json:"last_failure,omitempty"`
}
