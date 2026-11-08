package thsauth

import (
	"context"
	"crypto/sha256"
	"net/http"
	"sort"
)

func sessionDigest(cookies []*http.Cookie, present bool) ([sha256.Size]byte, error) {
	if !present {
		return [sha256.Size]byte{}, nil
	}
	ordered := append([]*http.Cookie(nil), cookies...)
	for _, cookie := range ordered {
		if err := validateCookie(cookie); err != nil {
			return [sha256.Size]byte{}, err
		}
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		if ordered[left].Domain != ordered[right].Domain {
			return ordered[left].Domain < ordered[right].Domain
		}
		if ordered[left].Path != ordered[right].Path {
			return ordered[left].Path < ordered[right].Path
		}
		return ordered[left].Name < ordered[right].Name
	})
	payload, err := EncodeSessionCookies(ordered)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256([]byte(payload)), nil
}

func (manager *Manager) reloadSession(ctx context.Context) (bool, error) {
	if manager.session == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, manager.options.RequestTimeout)
	stop := context.AfterFunc(manager.rootContext, cancel)
	defer cancel()
	defer stop()
	manager.refreshMutex.Lock()
	defer manager.refreshMutex.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if manager.refresh != nil {
		return false, nil
	}
	manager.sessionMutex.Lock()
	defer manager.sessionMutex.Unlock()
	return manager.reloadSessionLocked(ctx)
}

func (manager *Manager) reloadSessionLocked(ctx context.Context) (bool, error) {
	cookies, present, err := manager.session.LoadSession(ctx)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if manager.isClosed() {
		return false, context.Canceled
	}
	digest, err := sessionDigest(cookies, present)
	if err != nil {
		return false, err
	}
	if digest == manager.sessionDigest {
		return false, nil
	}
	jar, err := newCookieJar()
	if err != nil {
		return false, err
	}
	if present {
		if err := installCookies(jar, cookies); err != nil {
			return false, err
		}
	}
	manager.installSessionLocked(jar)
	manager.sessionDigest = digest
	if hasCandidateCookies(jar) {
		manager.recordSuccess()
	} else {
		manager.stateMutex.Lock()
		if !manager.stopped {
			manager.state = StateExpired
		}
		manager.stateMutex.Unlock()
	}
	return true, nil
}

func (manager *Manager) installSessionLocked(jar *trackedCookieJar) {
	_, revision := jar.snapshot()
	manager.jar = &jarState{jar: jar, revision: revision}
	manager.sessionGeneration++
	manager.client = &http.Client{
		Transport:     manager.options.Transport,
		Timeout:       manager.options.RequestTimeout,
		Jar:           jar,
		CheckRedirect: rejectRedirect,
	}
}

func (manager *Manager) saveSessionLocked(ctx context.Context, cookies []*http.Cookie) error {
	digest, err := sessionDigest(cookies, true)
	if err != nil {
		return err
	}
	if err := manager.session.SaveSession(ctx, cookies); err != nil {
		return err
	}
	manager.sessionDigest = digest
	return nil
}
