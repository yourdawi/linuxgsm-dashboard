package backend

import (
	"context"
	"strings"
	"time"
)

const (
	// GitHub allows few requests without a token, and the text rarely changes.
	releaseNotesMaxAge     = time.Hour
	releaseNotesRetryAfter = 10 * time.Minute
)

// ReleaseNotes is the text written into the GitHub release of one version.
type ReleaseNotes struct {
	Version string `json:"version"`
	Body    string `json:"body"`
}

// ReleaseNotes returns the texts of the stable releases, newest first, from the GitHub API. When
// GitHub cannot be reached, the last known texts are returned with the error.
func (u *Updater) ReleaseNotes(ctx context.Context) ([]ReleaseNotes, error) {
	u.notesMu.Lock()
	defer u.notesMu.Unlock()
	if !u.notesFetched.IsZero() && time.Since(u.notesFetched) < releaseNotesMaxAge {
		return u.notes, nil
	}
	if !u.notesFailed.IsZero() && time.Since(u.notesFailed) < releaseNotesRetryAfter {
		return u.notes, u.notesErr
	}

	var releases []struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Body       string `json:"body"`
	}
	if err := u.getJSON(ctx, u.apiBase()+"/repos/"+u.Repo+"/releases?per_page=30", &releases); err != nil {
		u.notesFailed, u.notesErr = time.Now(), err
		return u.notes, err
	}
	notes := []ReleaseNotes{}
	for _, r := range releases {
		if r.Draft || r.Prerelease || !IsStableReleaseTag(r.TagName) || strings.TrimSpace(r.Body) == "" {
			continue
		}
		body := r.Body
		if len(body) > maxReleaseNotes {
			body = strings.ToValidUTF8(body[:maxReleaseNotes], "")
		}
		notes = append(notes, ReleaseNotes{Version: r.TagName, Body: body})
	}
	u.notes, u.notesFetched, u.notesFailed, u.notesErr = notes, time.Now(), time.Time{}, nil
	return notes, nil
}

// UnseenReleaseNotes returns the notes of the versions up to current that came after seen. Without
// a seen version, which is every administrator after updating from a version without this feature,
// only the current one counts. A build without a release version (development, built from source)
// has no notes of its own.
func UnseenReleaseNotes(all []ReleaseNotes, seen, current string) []ReleaseNotes {
	if _, ok := ParseVersion(current); !ok {
		return nil
	}
	if _, ok := ParseVersion(seen); !ok {
		seen = ""
	}
	unseen := []ReleaseNotes{}
	for _, n := range all {
		if IsNewerVersion(n.Version, current) {
			continue
		}
		if seen == "" {
			if !IsNewerVersion(current, n.Version) {
				unseen = append(unseen, n)
			}
		} else if IsNewerVersion(n.Version, seen) {
			unseen = append(unseen, n)
		}
	}
	return unseen
}
