package backend

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ValidateInstall checks an installation request with the active runtime.
func (im *InstanceManager) ValidateInstall(gameCmd, username, installDir string) error {
	return im.rt.ValidateInstall(InstallRequest{GameCmd: gameCmd, Username: username, Dir: installDir})
}

// InstallGame installs a new game server through the runtime and streams the progress.
// installDir is optional; when empty the runtime picks the default location.
func (im *InstanceManager) InstallGame(w http.ResponseWriter, r *http.Request, gameCmd, username, password, installDir, lang string) {
	req := InstallRequest{GameCmd: gameCmd, Username: username, Password: password, Dir: installDir, Lang: lang}
	if err := im.rt.ValidateInstall(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sse, ok := newSSEWriter(w)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// The installation keeps running when the browser disconnects.
	err := im.rt.Install(context.Background(), req, sse.log)

	// Trigger reload
	im.ScanInstances()

	sse.exit(ExitCode(err))
}

// -------------------------------------------------------------
// Multi-Instance Creator
// -------------------------------------------------------------

var reInstanceSuffix = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func (im *InstanceManager) CreateMultiInstance(serverID string, suffix string) (string, error) {
	srv, ok := im.getServer(serverID)
	if !ok {
		return "", fmt.Errorf("server not found")
	}

	cleanSuffix := strings.ToLower(strings.TrimSpace(suffix))
	if cleanSuffix == "" {
		return "", fmt.Errorf("suffix cannot be empty")
	}
	if !reInstanceSuffix.MatchString(cleanSuffix) {
		return "", fmt.Errorf("suffix can only contain lowercase letters, numbers, and hyphens")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	newID, err := im.rt.Clone(ctx, srv, cleanSuffix)
	if err != nil {
		return "", err
	}

	im.ScanInstances()
	return newID, nil
}
