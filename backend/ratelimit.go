package backend

import (
	"sync"
	"time"
)

// LoginLimiter throttles failed logins per key (source IP and username) with a progressive
// cooldown. It never locks an account permanently: the cooldown is capped and failures
// expire, so an attacker cannot lock out a legitimate user forever.
type LoginLimiter struct {
	mu          sync.Mutex
	entries     map[string]*limiterEntry
	freeTries   int
	baseDelay   time.Duration
	maxDelay    time.Duration
	forgetAfter time.Duration
	maxEntries  int
	now         func() time.Time
	inflight    map[string]int // attempts being verified right now (Reserve)
}

type limiterEntry struct {
	failures    int
	lastFailure time.Time
	blockedTill time.Time
}

// NewLoginLimiter returns a limiter allowing 5 free failures, then doubling cooldowns from 2s up to 15m.
func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{
		entries:     make(map[string]*limiterEntry),
		freeTries:   5,
		baseDelay:   2 * time.Second,
		maxDelay:    15 * time.Minute,
		forgetAfter: 30 * time.Minute,
		maxEntries:  10000,
		now:         time.Now,
	}
}

// Allow reports whether a login attempt for all keys is currently permitted and, if not,
// how long the caller has to wait.
func (l *LoginLimiter) Allow(keys ...string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var wait time.Duration
	for _, k := range keys {
		e, ok := l.entries[k]
		if !ok {
			continue
		}
		if now.Sub(e.lastFailure) > l.forgetAfter {
			delete(l.entries, k)
			continue
		}
		if now.Before(e.blockedTill) {
			if d := e.blockedTill.Sub(now); d > wait {
				wait = d
			}
		}
	}
	return wait == 0, wait
}

// Failure records a failed attempt for every key.
func (l *LoginLimiter) Failure(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.entries) >= l.maxEntries {
		l.pruneLocked(now)
	}
	for _, k := range keys {
		e, ok := l.entries[k]
		if !ok || now.Sub(e.lastFailure) > l.forgetAfter {
			if !ok && len(l.entries) >= l.maxEntries {
				continue // bounded memory: drop tracking of new keys under extreme load
			}
			e = &limiterEntry{}
			l.entries[k] = e
		}
		e.failures++
		e.lastFailure = now
		if e.failures > l.freeTries {
			delay := l.baseDelay
			for i := 1; i < e.failures-l.freeTries && delay < l.maxDelay; i++ {
				delay *= 2
			}
			if delay > l.maxDelay {
				delay = l.maxDelay
			}
			e.blockedTill = now.Add(delay)
		}
	}
}

// Success clears the failure history of the given keys.
func (l *LoginLimiter) Success(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.entries, k)
	}
}

func (l *LoginLimiter) pruneLocked(now time.Time) {
	for k, e := range l.entries {
		if now.Sub(e.lastFailure) > l.forgetAfter && now.After(e.blockedTill) {
			delete(l.entries, k)
		}
	}
	// Still full: drop roughly half of the entries to keep memory bounded.
	if len(l.entries) >= l.maxEntries {
		i := 0
		for k := range l.entries {
			if i%2 == 0 {
				delete(l.entries, k)
			}
			i++
		}
	}
}

// Reserve is Allow for an attempt that is verified afterwards. The attempt counts while it runs,
// so parallel requests cannot all pass before the first failure is recorded: as long as a key has
// free tries left, at most that many attempts run at once, after that only one. done records the
// result (success clears the history, a failure counts).
func (l *LoginLimiter) Reserve(keys ...string) (ok bool, wait time.Duration, done func(success bool)) {
	if ok, wait := l.Allow(keys...); !ok {
		return false, wait, nil
	}
	l.mu.Lock()
	if l.inflight == nil {
		l.inflight = make(map[string]int)
	}
	for _, k := range keys {
		failures := 0
		if e, found := l.entries[k]; found {
			failures = e.failures
		}
		if n := l.inflight[k]; n > 0 && failures+n >= l.freeTries {
			l.mu.Unlock()
			return false, l.baseDelay, nil
		}
	}
	for _, k := range keys {
		l.inflight[k]++
	}
	l.mu.Unlock()

	var once sync.Once
	return true, 0, func(success bool) {
		once.Do(func() {
			l.mu.Lock()
			for _, k := range keys {
				if l.inflight[k]--; l.inflight[k] <= 0 {
					delete(l.inflight, k)
				}
			}
			l.mu.Unlock()
			if success {
				l.Success(keys...)
			} else {
				l.Failure(keys...)
			}
		})
	}
}
