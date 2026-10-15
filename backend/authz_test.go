package backend

import (
	"errors"
	"testing"
	"time"
)

func TestAuthorizeServerActionMatrix(t *testing.T) {
	exists := func(id string) bool { return id == "srv1" || id == "srv2" }

	admin := User{Username: "admin", Role: RoleAdmin}
	viewer := User{Username: "viewer", Role: RoleUser, Servers: []string{"srv1"}}
	operator := User{Username: "op", Role: RoleUser, Servers: []string{"srv1"}, Permissions: []string{PermStart, PermStop}}
	full := User{Username: "full", Role: RoleUser, Servers: []string{"srv1"}, Permissions: AssignablePermissions}

	type tc struct {
		user   *User
		server string
		perm   string
		want   error
	}
	cases := []tc{
		{nil, "srv1", PermView, ErrUnauthenticated},
		{&User{}, "srv1", PermView, ErrUnauthenticated},
		// admin
		{&admin, "srv1", PermAdmin, nil},
		{&admin, "srv2", PermConsole, nil},
		{&admin, "unknown", PermView, ErrServerNotFound},
		{&admin, "../etc", PermView, ErrServerNotFound},
		// viewer: read only
		{&viewer, "srv1", PermView, nil},
		{&viewer, "srv1", PermStart, ErrForbidden},
		{&viewer, "srv1", PermConsole, ErrForbidden},
		{&viewer, "srv1", PermConfig, ErrForbidden},
		{&viewer, "srv1", PermFiles, ErrForbidden},
		{&viewer, "srv1", PermBackup, ErrForbidden},
		{&viewer, "srv1", PermSchedule, ErrForbidden},
		{&viewer, "srv1", PermAlerts, ErrForbidden},
		{&viewer, "srv1", PermAdmin, ErrForbidden},
		// operator: only assigned permissions and servers
		{&operator, "srv1", PermStart, nil},
		{&operator, "srv1", PermRestart, ErrForbidden},
		{&operator, "srv2", PermStart, ErrForbidden},
		{&operator, "unknown", PermStart, ErrForbidden}, // no enumeration of foreign IDs
		// all assignable permissions still never grant admin-only actions
		{&full, "srv1", PermAlerts, nil},
		{&full, "srv1", PermAdmin, ErrForbidden},
		{&full, "srv2", PermView, ErrForbidden},
		// empty permission is never granted
		{&full, "srv1", "", ErrForbidden},
	}
	for i, c := range cases {
		got := AuthorizeServerAction(c.user, c.server, c.perm, exists)
		if !errors.Is(got, c.want) && !(got == nil && c.want == nil) {
			t.Errorf("case %d (%v %s %s): got %v want %v", i, c.user, c.server, c.perm, got, c.want)
		}
	}
}

func TestActionPermissionAllowlist(t *testing.T) {
	want := map[string]string{
		"start": PermStart, "stop": PermStop, "restart": PermRestart, "backup": PermBackup,
		"update": PermAdmin, "full-wipe": PermAdmin, "postdetails": PermAdmin, "debug": PermAdmin,
	}
	for action, perm := range want {
		got, ok := ActionPermission(action)
		if !ok || got != perm {
			t.Errorf("ActionPermission(%q) = %q,%v want %q", action, got, ok, perm)
		}
	}
	for _, a := range []string{"", "rm", "start; id", "install", "auto-install", "console"} {
		if _, ok := ActionPermission(a); ok {
			t.Errorf("unexpected action allowed: %q", a)
		}
	}
	for _, a := range []string{"full-wipe", "debug", "details"} {
		if _, ok := BulkActionPermission(a); ok {
			t.Errorf("bulk action should not be allowed: %q", a)
		}
	}
}

func TestLoginLimiterProgressiveCooldown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := NewLoginLimiter()
	l.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		if ok, _ := l.Allow("ip:1"); !ok {
			t.Fatalf("attempt %d blocked too early", i)
		}
		l.Failure("ip:1")
	}
	if ok, _ := l.Allow("ip:1"); !ok {
		t.Fatal("5 free attempts expected")
	}
	l.Failure("ip:1")
	ok, wait := l.Allow("ip:1")
	if ok || wait <= 0 {
		t.Fatal("expected cooldown after 6 failures")
	}
	// other keys are not affected
	if ok, _ := l.Allow("ip:2"); !ok {
		t.Fatal("unrelated key blocked")
	}
	// cooldown expires (no permanent lockout)
	now = now.Add(wait + time.Second)
	if ok, _ := l.Allow("ip:1"); !ok {
		t.Fatal("cooldown did not expire")
	}
	// cooldown is capped
	for i := 0; i < 50; i++ {
		l.Failure("ip:1")
	}
	_, wait = l.Allow("ip:1")
	if wait > l.maxDelay {
		t.Fatalf("cooldown exceeds cap: %v", wait)
	}
	l.Success("ip:1")
	if ok, _ := l.Allow("ip:1"); !ok {
		t.Fatal("success must reset")
	}
}

func TestLoginLimiterBoundedMemory(t *testing.T) {
	l := NewLoginLimiter()
	l.maxEntries = 100
	for i := 0; i < 1000; i++ {
		l.Failure(time.Duration(i).String())
	}
	if len(l.entries) > 100 {
		t.Fatalf("limiter grew unbounded: %d", len(l.entries))
	}
}
