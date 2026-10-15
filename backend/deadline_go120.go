//go:build go1.20

package backend

import (
	"net/http"
	"time"
)

// SetRequestDeadlines applies per-request read and write deadlines.
func SetRequestDeadlines(w http.ResponseWriter, read, write time.Duration) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(read))
	_ = rc.SetWriteDeadline(time.Now().Add(write))
}

// DisableWriteDeadline removes the per-request write deadline for long running responses
// (SSE streams, large downloads).
func DisableWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
}

// DisableReadDeadline removes the per-request read deadline for large uploads.
func DisableReadDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
}
