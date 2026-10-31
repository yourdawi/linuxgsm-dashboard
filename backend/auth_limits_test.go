package backend

import (
	"fmt"
	"sync"
	"testing"
)

// Parallel attempts must not all pass before the first failure is recorded.
func TestLimiterReserveCountsAttemptsInFlight(t *testing.T) {
	l := NewLoginLimiter()
	var mu sync.Mutex
	var dones []func(bool)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Go(func() {
			if ok, _, done := l.Reserve("user:admin"); ok {
				mu.Lock()
				dones = append(dones, done)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(dones) != l.freeTries {
		t.Fatalf("%d attempts ran at once, want %d", len(dones), l.freeTries)
	}
	for _, d := range dones {
		d(false)
	}
	// The free tries are used up: one more attempt runs, its failure starts the cooldown.
	ok, _, done := l.Reserve("user:admin")
	if !ok {
		t.Fatal("attempt after the free tries refused")
	}
	if ok2, _, _ := l.Reserve("user:admin"); ok2 {
		t.Fatal("a second attempt ran in parallel after the free tries")
	}
	done(false)
	if ok, _, _ := l.Reserve("user:admin"); ok {
		t.Fatal("allowed during the cooldown")
	}
	// A success clears the key.
	l2 := NewLoginLimiter()
	_, _, done = l2.Reserve("user:x")
	done(true)
	done(false) // only the first call counts
	if e := l2.entries["user:x"]; e != nil {
		t.Fatalf("entry left after success: %+v", e)
	}
}

func TestAuditLogKeepsUserActionsDuringFailedLoginBursts(t *testing.T) {
	var entries []AuditEntry
	add := func(action string) { entries = trimAuditLog(append([]AuditEntry{{Action: action}}, entries...)) }
	for i := 0; i < 50; i++ {
		add("SERVER_DELETE")
	}
	for i := 0; i < 5000; i++ {
		add("LOGIN_FAILED")
	}
	kept, failed := 0, 0
	for _, e := range entries {
		switch e.Action {
		case "SERVER_DELETE":
			kept++
		case "LOGIN_FAILED":
			failed++
		}
	}
	if kept != 50 || failed != maxAuditFailedLogins {
		t.Fatalf("kept %d user actions and %d failed logins", kept, failed)
	}
	for i := 0; i < 2000; i++ {
		add(fmt.Sprintf("ACTION_%d", i))
	}
	if len(entries) != maxAuditEntries {
		t.Fatalf("log size %d", len(entries))
	}
}
