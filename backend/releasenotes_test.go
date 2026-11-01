package backend

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestReleaseNotesComeFromGitHub(t *testing.T) {
	gh := newFakeGitHub(t, "v2.1.0")
	gh.releases = []map[string]interface{}{
		{"tag_name": "v2.1.0", "body": "**New**\r\n- Thing"},
		{"tag_name": "v2.1.0-rc1", "body": "not a stable tag"},
		{"tag_name": "v2.0.2", "prerelease": true, "body": "prerelease"},
		{"tag_name": "v2.0.1", "draft": true, "body": "draft"},
		{"tag_name": "v2.0.0", "body": "   "},
		{"tag_name": "v1.9.0", "body": strings.Repeat("ä", maxReleaseNotes)},
	}
	u := gh.updater("2.1.0")
	notes, err := u.ReleaseNotes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || notes[0].Version != "v2.1.0" || notes[0].Body != "**New**\r\n- Thing" || notes[1].Version != "v1.9.0" {
		t.Fatalf("notes = %+v", notes)
	}
	if len(notes[1].Body) > maxReleaseNotes || !utf8.ValidString(notes[1].Body) {
		t.Errorf("long text: %d bytes, valid %v", len(notes[1].Body), utf8.ValidString(notes[1].Body))
	}

	// GitHub allows few requests without a token.
	for i := 0; i < 5; i++ {
		if _, err := u.ReleaseNotes(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := gh.listCalls.Load(); n != 1 {
		t.Errorf("requests = %d", n)
	}
}

func TestReleaseNotesWithoutGitHub(t *testing.T) {
	gh := newFakeGitHub(t, "v2.1.0")
	gh.releases = []map[string]interface{}{{"tag_name": "v2.1.0", "body": "text"}}
	gh.listStatus = http.StatusForbidden // the limit for requests without a token
	u := gh.updater("2.1.0")

	notes, err := u.ReleaseNotes(context.Background())
	if err == nil || len(notes) != 0 {
		t.Fatalf("notes %+v, err %v", notes, err)
	}
	if _, err := u.ReleaseNotes(context.Background()); err == nil {
		t.Error("error forgotten within the waiting time")
	}
	if n := gh.listCalls.Load(); n != 1 {
		t.Errorf("a failed request must not be repeated right away: %d", n)
	}

	// Later it works, and a later failure keeps what is known.
	gh.listStatus = 0
	u.notesFailed = time.Now().Add(-2 * releaseNotesRetryAfter)
	if notes, err = u.ReleaseNotes(context.Background()); err != nil || len(notes) != 1 {
		t.Fatalf("after the failure: %+v, %v", notes, err)
	}
	gh.listStatus = http.StatusBadGateway
	u.notesFetched = time.Now().Add(-2 * releaseNotesMaxAge)
	if notes, err = u.ReleaseNotes(context.Background()); err == nil || len(notes) != 1 {
		t.Fatalf("known notes lost: %+v, %v", notes, err)
	}
}

func TestUnseenReleaseNotes(t *testing.T) {
	all := []ReleaseNotes{{"v2.1.0", "a"}, {"v2.0.1", "b"}, {"v2.0.0", "c"}}
	versions := func(n []ReleaseNotes) []string {
		out := []string{}
		for _, e := range n {
			out = append(out, e.Version)
		}
		return out
	}
	for _, c := range []struct {
		name, seen, current string
		want                []string
	}{
		{"one update", "2.0.1", "2.1.0", []string{"v2.1.0"}},
		{"several updates at once", "2.0.0", "2.1.0", []string{"v2.1.0", "v2.0.1"}},
		{"already seen", "2.1.0", "2.1.0", []string{}},
		{"from a version without this feature", "", "2.0.1", []string{"v2.0.1"}},
		{"notes of a later version stay hidden", "2.0.0", "2.0.1", []string{"v2.0.1"}},
		{"a downgrade shows nothing", "2.1.0", "2.0.1", []string{}},
		{"a tag with v", "v2.0.1", "v2.1.0", []string{"v2.1.0"}},
		{"development build", "2.0.0", "dev-abc", []string{}},
		{"built from source", "2.0.0", "dev", []string{}},
		{"first release after a source build", "dev", "2.0.1", []string{"v2.0.1"}},
		{"no release for this version", "2.1.0", "2.2.0", []string{}},
	} {
		if got := versions(UnseenReleaseNotes(all, c.seen, c.current)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if got := UnseenReleaseNotes(nil, "2.0.0", "2.0.1"); got == nil || len(got) != 0 {
		t.Errorf("without notes: %v", got)
	}
}

func TestNotesSeenIsKeptPerUser(t *testing.T) {
	am, err := NewAuthManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !am.IsFreshInstall() {
		t.Error("first start not recognised")
	}
	if err := am.SetNotesSeen("admin", "2.0.1"); err != nil {
		t.Fatal(err)
	}
	if u, _ := am.GetUser("admin"); u.NotesSeen != "2.0.1" {
		t.Errorf("seen = %q", u.NotesSeen)
	}
	reopened, err := NewAuthManager(am.ConfigDir())
	if err != nil {
		t.Fatal(err)
	}
	if reopened.IsFreshInstall() {
		t.Error("second start counted as first")
	}
	if u, _ := reopened.GetUser("admin"); u.NotesSeen != "2.0.1" {
		t.Errorf("seen after a restart = %q", u.NotesSeen)
	}
	if err := reopened.SetNotesSeen("nobody", "2.0.1"); err == nil {
		t.Error("unknown user accepted")
	}
}
