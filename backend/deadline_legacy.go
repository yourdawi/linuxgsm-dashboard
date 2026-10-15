//go:build !go1.20

package backend

import (
	"net/http"
	"time"
)

// Go < 1.20 (e.g. the distribution packages of Debian 12 / Ubuntu 22.04) has no
// http.ResponseController. Per-request deadlines are skipped there; the server still enforces
// ReadHeaderTimeout and IdleTimeout.

func SetRequestDeadlines(w http.ResponseWriter, read, write time.Duration) {}

func DisableWriteDeadline(w http.ResponseWriter) {}

func DisableReadDeadline(w http.ResponseWriter) {}
