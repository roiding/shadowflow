package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

func bearerMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return bearerAuth(token, next)
	}
}

func bearerAuth(token string, next http.Handler) http.Handler {
	expected := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		parts := strings.Fields(authorization)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") ||
			subtle.ConstantTimeCompare([]byte(parts[1]), expected) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type clientBucket struct {
	tokens   float64
	lastFill time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	limit   float64
	burst   float64
	buckets map[string]*clientBucket
	lastGC  time.Time
}

func newRateLimiter(perMinute int) *rateLimiter {
	if perMinute <= 0 {
		perMinute = 120
	}
	return &rateLimiter{
		limit:   float64(perMinute) / 60,
		burst:   float64(perMinute),
		buckets: make(map[string]*clientBucket),
		lastGC:  time.Now(),
	}
}

func (l *rateLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastGC) > time.Minute {
		for id, bucket := range l.buckets {
			if now.Sub(bucket.lastFill) > 10*time.Minute {
				delete(l.buckets, id)
			}
		}
		l.lastGC = now
	}
	bucket := l.buckets[key]
	if bucket == nil {
		bucket = &clientBucket{tokens: l.burst, lastFill: now}
		l.buckets[key] = bucket
	}
	bucket.tokens += now.Sub(bucket.lastFill).Seconds() * l.limit
	if bucket.tokens > l.burst {
		bucket.tokens = l.burst
	}
	bucket.lastFill = now
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func requestTimeout(duration time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, duration, `{"error":{"code":"timeout","message":"request timed out"}}`)
	}
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics" {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
