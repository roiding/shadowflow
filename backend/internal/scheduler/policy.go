package scheduler

import "time"

type jobPolicy struct {
	timeout     time.Duration
	maxAttempts int
	retryAfter  time.Duration
}

func policyFor(kind string) jobPolicy {
	switch kind {
	case "minute":
		return jobPolicy{timeout: 50 * time.Second, maxAttempts: 2, retryAfter: 15 * time.Second}
	case "end-of-day":
		return jobPolicy{timeout: 15 * time.Minute, maxAttempts: 5, retryAfter: time.Minute}
	case "end-of-day-industry", "end-of-day-concept":
		return jobPolicy{timeout: 15 * time.Minute, maxAttempts: 5, retryAfter: time.Minute}
	case "end-of-day-stock":
		return jobPolicy{timeout: 30 * time.Minute, maxAttempts: 5, retryAfter: time.Minute}
	case "stock-kline":
		return jobPolicy{timeout: 90 * time.Minute, maxAttempts: 4, retryAfter: 5 * time.Minute}
	case "relations":
		return jobPolicy{timeout: 45 * time.Minute, maxAttempts: 3, retryAfter: 5 * time.Minute}
	case "cleanup", "maintenance":
		return jobPolicy{timeout: 2 * time.Minute, maxAttempts: 2, retryAfter: time.Minute}
	case "startup-recovery":
		return jobPolicy{timeout: 105 * time.Minute, maxAttempts: 1}
	default:
		return jobPolicy{timeout: time.Minute, maxAttempts: 1}
	}
}
