package thsauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	sessionVersion  = 1
	maxSessionBytes = 1 << 20
	maxCookieCount  = 256
)

type sessionFile struct {
	Version int             `json:"version"`
	Cookies []sessionCookie `json:"cookies"`
}

type sessionCookie struct {
	Name        string        `json:"name"`
	Value       string        `json:"value"`
	Quoted      bool          `json:"quoted,omitempty"`
	Path        string        `json:"path,omitempty"`
	Domain      string        `json:"domain"`
	Expires     *time.Time    `json:"expires,omitempty"`
	RawExpires  string        `json:"raw_expires,omitempty"`
	MaxAge      int           `json:"max_age,omitempty"`
	Secure      bool          `json:"secure,omitempty"`
	HttpOnly    bool          `json:"http_only,omitempty"`
	SameSite    http.SameSite `json:"same_site,omitempty"`
	Partitioned bool          `json:"partitioned,omitempty"`
}

func readSession(path string) ([]*http.Cookie, bool, error) {
	if path == "" {
		return nil, false, nil
	}
	if !filepath.IsAbs(path) {
		return nil, false, fmt.Errorf("%w: session path must be absolute", ErrUnavailable)
	}
	parent := filepath.Dir(path)
	parentInfo, parentError := os.Lstat(parent)
	if errors.Is(parentError, os.ErrNotExist) {
		return nil, false, nil
	}
	if parentError != nil {
		return nil, false, fmt.Errorf("%w: inspect session directory", ErrUnavailable)
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o077 != 0 {
		return nil, false, fmt.Errorf("%w: session directory must be private", ErrUnavailable)
	}
	descriptor, openError := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(openError, os.ErrNotExist) {
		return nil, false, nil
	}
	if openError != nil {
		return nil, false, fmt.Errorf("%w: open session file", ErrUnavailable)
	}
	file := os.NewFile(uintptr(descriptor), "ths-session")
	defer file.Close()
	info, statError := file.Stat()
	if statError != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > maxSessionBytes {
		return nil, false, fmt.Errorf("%w: session file must be private, regular and bounded", ErrUnavailable)
	}
	body, readError := io.ReadAll(io.LimitReader(file, maxSessionBytes+1))
	if readError != nil || len(body) == 0 || len(body) > maxSessionBytes {
		return nil, false, fmt.Errorf("%w: unreadable session file", ErrUnavailable)
	}
	cookies, decodeError := DecodeSessionCookies(string(body))
	if decodeError != nil {
		return nil, false, decodeError
	}
	return cookies, true, nil
}

func writeSession(path string, cookies []*http.Cookie) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: session path must be absolute", ErrUnavailable)
	}
	body, encodeError := EncodeSessionCookies(cookies)
	if encodeError != nil {
		return encodeError
	}
	parent := filepath.Dir(path)
	if mkdirError := os.MkdirAll(parent, 0o700); mkdirError != nil {
		return fmt.Errorf("%w: create session directory", ErrUnavailable)
	}
	parentInfo, parentError := os.Lstat(parent)
	if parentError != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: unsafe session directory", ErrUnavailable)
	}
	if linkInfo, linkError := os.Lstat(path); linkError == nil {
		if !linkInfo.Mode().IsRegular() || linkInfo.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%w: existing session file is unsafe", ErrUnavailable)
		}
	} else if !errors.Is(linkError, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect session file", ErrUnavailable)
	}
	temporary, createError := os.CreateTemp(parent, "."+filepath.Base(path)+".tmp-")
	if createError != nil {
		return fmt.Errorf("%w: create temporary session", ErrUnavailable)
	}
	temporaryPath := temporary.Name()
	failed := true
	defer func() {
		if failed {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	if chmodError := temporary.Chmod(0o600); chmodError != nil {
		return fmt.Errorf("%w: secure temporary session", ErrUnavailable)
	}
	if _, writeError := temporary.Write([]byte(body)); writeError != nil {
		return fmt.Errorf("%w: write temporary session", ErrUnavailable)
	}
	if syncError := temporary.Sync(); syncError != nil {
		return fmt.Errorf("%w: sync temporary session", ErrUnavailable)
	}
	if closeError := temporary.Close(); closeError != nil {
		return fmt.Errorf("%w: close temporary session", ErrUnavailable)
	}
	if renameError := os.Rename(temporaryPath, path); renameError != nil {
		return fmt.Errorf("%w: replace session file", ErrUnavailable)
	}
	failed = false
	if chmodError := os.Chmod(path, 0o600); chmodError != nil {
		return fmt.Errorf("%w: secure session file", ErrUnavailable)
	}
	if directory, openError := os.Open(parent); openError == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

// EncodeSessionCookies produces the versioned session JSON shared by the file
// and database session stores.
func EncodeSessionCookies(cookies []*http.Cookie) (string, error) {
	if len(cookies) > maxCookieCount {
		return "", fmt.Errorf("%w: invalid session cookie count", ErrUnavailable)
	}
	stored := sessionFile{Version: sessionVersion, Cookies: []sessionCookie{}}
	for _, cookie := range cookies {
		if validateError := validateCookie(cookie); validateError != nil {
			return "", validateError
		}
		item := sessionCookie{
			Name:        cookie.Name,
			Value:       cookie.Value,
			Quoted:      cookie.Quoted,
			Path:        cookie.Path,
			Domain:      cookie.Domain,
			RawExpires:  cookie.RawExpires,
			MaxAge:      cookie.MaxAge,
			Secure:      cookie.Secure,
			HttpOnly:    cookie.HttpOnly,
			SameSite:    cookie.SameSite,
			Partitioned: cookie.Partitioned,
		}
		if !cookie.Expires.IsZero() {
			expiry := cookie.Expires.UTC()
			item.Expires = &expiry
		}
		stored.Cookies = append(stored.Cookies, item)
	}
	body, marshalError := json.Marshal(stored)
	if marshalError != nil || len(body) >= maxSessionBytes {
		return "", fmt.Errorf("%w: encode session", ErrUnavailable)
	}
	return string(body), nil
}

func DecodeSessionCookies(payload string) ([]*http.Cookie, error) {
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	var stored sessionFile
	if decodeError := decoder.Decode(&stored); decodeError != nil {
		return nil, fmt.Errorf("%w: invalid session payload", ErrUnavailable)
	}
	var extra any
	if decodeError := decoder.Decode(&extra); decodeError != io.EOF {
		return nil, fmt.Errorf("%w: invalid session payload", ErrUnavailable)
	}
	if stored.Version != sessionVersion || len(stored.Cookies) > maxCookieCount {
		return nil, fmt.Errorf("%w: unsupported session payload", ErrUnavailable)
	}
	cookies := make([]*http.Cookie, 0, len(stored.Cookies))
	for _, item := range stored.Cookies {
		cookie := &http.Cookie{
			Name:        item.Name,
			Value:       item.Value,
			Quoted:      item.Quoted,
			Path:        item.Path,
			Domain:      item.Domain,
			RawExpires:  item.RawExpires,
			MaxAge:      item.MaxAge,
			Secure:      item.Secure,
			HttpOnly:    item.HttpOnly,
			SameSite:    item.SameSite,
			Partitioned: item.Partitioned,
		}
		if item.Expires != nil {
			cookie.Expires = item.Expires.UTC()
		}
		if validateError := validateCookie(cookie); validateError != nil {
			return nil, validateError
		}
		cookies = append(cookies, cookie)
	}
	return cookies, nil
}

// fileSessionStore keeps the login state in the hardened session JSON file.
type fileSessionStore struct{ path string }

func (store fileSessionStore) LoadSession(ctx context.Context) ([]*http.Cookie, bool, error) {
	return readSession(store.path)
}

func (store fileSessionStore) SaveSession(ctx context.Context, cookies []*http.Cookie) error {
	return writeSession(store.path, cookies)
}

func validateCookie(cookie *http.Cookie) error {
	if cookie == nil || cookie.Path == "" {
		return fmt.Errorf("%w: session cookie requires domain and path", ErrCredentials)
	}
	domain := strings.ToLower(strings.TrimPrefix(cookie.Domain, "."))
	if domain == "" {
		return fmt.Errorf("%w: session cookie requires a domain", ErrCredentials)
	}
	if domain != "10jqka.com.cn" && !strings.HasSuffix(domain, ".10jqka.com.cn") {
		return fmt.Errorf("%w: session cookie has an unapproved domain", ErrCredentials)
	}
	normalized := *cookie
	normalized.Domain = domain
	if validError := normalized.Valid(); validError != nil {
		return fmt.Errorf("%w: invalid session cookie", ErrCredentials)
	}
	return nil
}

func scrubCookies(cookies []*http.Cookie) {
	for _, cookie := range cookies {
		if cookie == nil {
			continue
		}
		cookie.Name = ""
		cookie.Value = ""
		cookie.Path = ""
		cookie.Domain = ""
		cookie.Expires = time.Time{}
		cookie.RawExpires = ""
		cookie.MaxAge = 0
		cookie.Secure = false
		cookie.HttpOnly = false
		cookie.SameSite = http.SameSiteDefaultMode
		cookie.Partitioned = false
		cookie.Raw = ""
		cookie.Unparsed = nil
	}
}

func isAuthStatus(status int) bool {
	return status == http.StatusUnauthorized
}

type trackedCookieJar struct {
	mutex    sync.Mutex
	delegate http.CookieJar
	cookies  map[string]*http.Cookie
	revision uint64
}

func newCookieJar() (*trackedCookieJar, error) {
	delegate, createError := cookiejar.New(nil)
	if createError != nil {
		return nil, createError
	}
	jar := &trackedCookieJar{delegate: delegate, cookies: make(map[string]*http.Cookie)}
	return jar, nil
}

func (jar *trackedCookieJar) Cookies(target *url.URL) []*http.Cookie {
	jar.mutex.Lock()
	defer jar.mutex.Unlock()
	return jar.delegate.Cookies(target)
}

func (jar *trackedCookieJar) SetCookies(target *url.URL, cookies []*http.Cookie) {
	if target == nil || target.Scheme != "https" {
		return
	}
	jar.mutex.Lock()
	defer jar.mutex.Unlock()
	for _, received := range cookies {
		if received == nil {
			continue
		}
		cookie := cloneCookie(received)
		host := strings.ToLower(target.Hostname())
		domain := strings.ToLower(strings.TrimPrefix(cookie.Domain, "."))
		hostOnly := domain == ""
		if hostOnly {
			domain = host
		}
		if host != domain && !strings.HasSuffix(host, "."+domain) {
			continue
		}
		cookie.Domain = domain
		if !hostOnly {
			cookie.Domain = "." + domain
		}
		if cookie.Path == "" || !strings.HasPrefix(cookie.Path, "/") {
			cookie.Path = "/"
			if separator := strings.LastIndex(target.Path, "/"); separator > 0 {
				cookie.Path = target.Path[:separator]
			}
		}
		if validateCookie(cookie) != nil {
			continue
		}
		jar.delegate.SetCookies(target, []*http.Cookie{received})
		key := domain + "\x00" + cookie.Path + "\x00" + cookie.Name
		if isCookieDeletion(cookie) {
			if _, exists := jar.cookies[key]; exists {
				delete(jar.cookies, key)
				jar.revision++
			}
			continue
		}
		if cookie.MaxAge > 0 {
			cookie.Expires = time.Now().UTC().Add(time.Duration(cookie.MaxAge) * time.Second)
			cookie.MaxAge = 0
		}
		if sameCookie(jar.cookies[key], cookie) {
			continue
		}
		jar.revision++
		jar.cookies[key] = cookie
	}
}

func sameCookie(previous, current *http.Cookie) bool {
	return previous != nil && current != nil && previous.Name == current.Name && previous.Value == current.Value &&
		previous.Quoted == current.Quoted && previous.Domain == current.Domain && previous.Path == current.Path &&
		previous.Expires.Equal(current.Expires) && previous.RawExpires == current.RawExpires && previous.MaxAge == current.MaxAge &&
		previous.Secure == current.Secure && previous.HttpOnly == current.HttpOnly && previous.SameSite == current.SameSite &&
		previous.Partitioned == current.Partitioned
}

func (jar *trackedCookieJar) snapshot() ([]*http.Cookie, uint64) {
	jar.mutex.Lock()
	defer jar.mutex.Unlock()
	keys := make([]string, 0, len(jar.cookies))
	for key, cookie := range jar.cookies {
		if isCookieDeletion(cookie) {
			delete(jar.cookies, key)
			jar.revision++
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	cookies := make([]*http.Cookie, 0, len(keys))
	for _, key := range keys {
		cookies = append(cookies, cloneCookie(jar.cookies[key]))
	}
	return cookies, jar.revision
}
