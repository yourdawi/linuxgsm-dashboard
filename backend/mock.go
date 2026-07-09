package backend

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"time"
)

// GetMockSystemStats generates simulated statistics for the host and game servers
func GetMockSystemStats(servers []GameServerInstance) *SystemStats {
	t := time.Now().Unix()
	
	// Create some oscillation based on current epoch time
	cpuOsc := 15.0 + 10.0*math.Sin(float64(t)/30.0) + rand.Float64()*3.0
	ramOsc := 30.0 + 3.0*math.Cos(float64(t)/60.0) + rand.Float64()*0.5
	
	ramTotal := 16.0
	ramUsed := (ramOsc / 100.0) * ramTotal
	
	diskTotal := 500.0
	diskFree := 342.1 // relatively static
	diskPercent := ((diskTotal - diskFree) / diskTotal) * 100.0

	var srvStats []ServerStats
	for _, srv := range servers {
		var cpu float64
		var ram float64
		var pids []int
		
		if srv.Status == "running" {
			// Specific mock usage per game
			if srv.ID == "arkserver" {
				cpu = 12.0 + 4.0*math.Sin(float64(t)/15.0) + rand.Float64()*2.0
				ram = 3.4 + rand.Float64()*0.1
				pids = []int{4201, 4202, 4203}
			} else if srv.ID == "vhserver" {
				cpu = 6.0 + 2.0*math.Cos(float64(t)/20.0) + rand.Float64()*1.0
				ram = 1.6 + rand.Float64()*0.05
				pids = []int{4310, 4311}
			} else {
				// Random fallback for custom servers
				cpu = 5.0 + rand.Float64()*10.0
				ram = 1.0 + rand.Float64()*2.0
				pids = []int{5000 + rand.Intn(100)}
			}
		}
		
		srvStats = append(srvStats, ServerStats{
			ID:     srv.ID,
			Name:   srv.Name,
			User:   srv.User,
			Status: srv.Status,
			CPU:    cpu,
			RAM:    ram,
			PIDs:   pids,
		})
	}

	return &SystemStats{
		Host: HostStats{
			CPU:         cpuOsc,
			CPUCores:    8,
			CPUModel:    "AMD Ryzen 7 5800X 8-Core Processor (Mock)",
			RAMUsed:     ramUsed,
			RAMTotal:    ramTotal,
			RAMPercent:  ramOsc,
			DiskFree:    diskFree,
			DiskTotal:   diskTotal,
			DiskPercent: diskPercent,
		},
		Servers: srvStats,
	}
}

// StreamMockAction writes a sequence of simulated console events to the client SSE stream
func StreamMockAction(w http.ResponseWriter, r *http.Request, action string, serverID string, updateStatusCallback func(string)) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sendSSE := func(eventType string, data interface{}) {
		jsonData, _ := json.Marshal(data)
		fmt.Fprintf(w, "data: %s\n\n", jsonData)
		flusher.Flush()
	}

	logLine := func(text string, delayMs int) {
		time.Sleep(time.Duration(delayMs) * time.Millisecond)
		sendSSE("message", map[string]interface{}{
			"type": "log",
			"text": text,
		})
	}

	switch action {
	case "start":
		updateStatusCallback("running")
		logLine(fmt.Sprintf("[%s] Starting lgsm instance %s...", time.Now().Format("15:04:05"), serverID), 100)
		logLine("Loading LinuxGSM configuration...", 200)
		logLine("Checking for system dependencies... OK", 300)
		logLine("Checking tmux session... not found", 150)
		logLine("Executing command: tmux new-session -d -s arkserver ./arkserver run", 400)
		logLine("Connecting to tmux console stream...", 200)
		logLine("Server executable started in background tmux screen.", 200)
		logLine("Waiting for server to open socket port...", 500)
		logLine("Port checks: Game Port [7777] - OPEN, Query Port [27015] - OPEN", 300)
		logLine(fmt.Sprintf("[%s] Server %s started successfully!", time.Now().Format("15:04:05"), serverID), 200)

	case "stop":
		updateStatusCallback("stopped")
		logLine(fmt.Sprintf("[%s] Stopping lgsm instance %s...", time.Now().Format("15:04:05"), serverID), 100)
		logLine("Locating game server process PID...", 150)
		logLine("Sending termination signal to game console... (SIGTERM)", 300)
		logLine("Waiting for game engine cleanup handlers...", 500)
		logLine("Tmux session 'arkserver' was killed.", 200)
		logLine(fmt.Sprintf("[%s] Server %s stopped successfully.", time.Now().Format("15:04:05"), serverID), 200)

	case "restart":
		logLine(fmt.Sprintf("[%s] Restarting lgsm instance %s...", time.Now().Format("15:04:05"), serverID), 100)
		logLine("Stopping server...", 200)
		time.Sleep(300 * time.Millisecond)
		logLine("Server stopped.", 100)
		updateStatusCallback("stopped")
		logLine("Starting server...", 400)
		time.Sleep(300 * time.Millisecond)
		updateStatusCallback("running")
		logLine("Server restarted successfully.", 200)

	case "update":
		updateStatusCallback("updating")
		logLine(fmt.Sprintf("[%s] Checking for updates on Steam for %s...", time.Now().Format("15:04:05"), serverID), 100)
		logLine("Logging into SteamCMD anonymously...", 300)
		logLine("AppInfo update request... OK", 200)
		logLine("Local Build ID: 12345678", 200)
		logLine("Remote Build ID: 12345678", 200)
		logLine("No update required. Your server is up to date.", 300)
		updateStatusCallback("stopped")
		logLine("Update process finished.", 100)

	case "backup":
		logLine(fmt.Sprintf("[%s] Starting backup for %s...", time.Now().Format("15:04:05"), serverID), 100)
		logLine("Calculating size of directory...", 200)
		logLine("A total of 4.2 GB will be compressed.", 200)
		logLine("Stopping server for safe backup...", 100)
		updateStatusCallback("stopped")
		logLine("Stopping tmux session... OK", 300)
		logLine("Creating backup file: /home/"+serverID+"/backups/"+serverID+"-backup.tar.gz", 500)
		logLine("Compressing file system... [10%] [45%] [80%] [100%]", 800)
		logLine("Backup file created successfully.", 200)
		logLine("Starting server back up...", 200)
		updateStatusCallback("running")
		logLine("Server is now running.", 100)
		logLine(fmt.Sprintf("[%s] Backup finished.", time.Now().Format("15:04:05")), 100)

	case "validate":
		logLine(fmt.Sprintf("[%s] Starting validation of server files for %s...", time.Now().Format("15:04:05"), serverID), 100)
		logLine("Logging into SteamCMD anonymously...", 200)
		logLine("Verifying file lists...", 300)
		logLine("Comparing local files with remote repository...", 400)
		logLine("Validating: [=====>                         ] 18%", 200)
		logLine("Validating: [===========>                   ] 38%", 200)
		logLine("Validating: [===================>           ] 65%", 200)
		logLine("Validating: [=========================>     ] 85%", 200)
		logLine("Validating: [==============================>] 100%", 200)
		logLine("Validation complete: 0 missing or corrupted files found.", 200)

	case "details":
		logLine(fmt.Sprintf("[%s] Querying details for %s...", time.Now().Format("15:04:05"), serverID), 100)
		logLine("==================================================================", 10)
		logLine("Distro Details", 20)
		logLine("==================================================================", 10)
		logLine("Distro:      Debian GNU/Linux 11 (bullseye)", 50)
		logLine("Arch:        x86_64", 50)
		logLine("Kernel:      5.10.0-18-amd64", 50)
		logLine("tmux:        tmux 3.1c", 50)
		logLine("", 10)
		logLine("==================================================================", 10)
		logLine("Performance", 20)
		logLine("==================================================================", 10)
		logLine("Uptime:      12d, 4h, 32m", 50)
		logLine("Avg Load:    0.24, 0.15, 0.08", 50)
		logLine("", 10)
		logLine("==================================================================", 10)
		logLine("Disk Usage", 20)
		logLine("==================================================================", 10)
		logLine("Disk available:   242.1G", 50)
		logLine("Serverfiles:      4.2G", 50)
		logLine("Backups:          1.2G", 50)
		logLine("", 10)
		logLine("==================================================================", 10)
		logLine("Server Details", 20)
		logLine("==================================================================", 10)
		logLine("Server name: Sleek "+serverID+" Server", 50)
		logLine("Server IP:   127.0.0.1:2456", 50)
		logLine("Status:      RUNNING", 50)
		logLine("User:        "+serverID, 50)
		logLine("Location:    /home/"+serverID, 50)
		logLine("Config:      /home/"+serverID+"/lgsm/config-lgsm/"+serverID+"/common.cfg", 50)
		logLine("", 10)
		logLine("==================================================================", 10)
	}

	// Send exit status
	sendSSE("message", map[string]interface{}{
		"type": "exit",
		"code": 0,
	})
}

// StreamMockInstall writes simulated logs for setting up a brand new gameserver user and downloading assets
func StreamMockInstall(w http.ResponseWriter, r *http.Request, gameCmd, username string, addServerCallback func(GameServerInstance)) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sendSSE := func(eventType string, data interface{}) {
		jsonData, _ := json.Marshal(data)
		fmt.Fprintf(w, "data: %s\n\n", jsonData)
		flusher.Flush()
	}

	logLine := func(text string, delayMs int) {
		time.Sleep(time.Duration(delayMs) * time.Millisecond)
		sendSSE("message", map[string]interface{}{
			"type": "log",
			"text": text,
		})
	}

	logLine("[INSTALL] Initializing unattended gameserver installation...", 100)
	logLine(fmt.Sprintf("[INSTALL] Creating new Linux user: '%s'...", username), 300)
	logLine(fmt.Sprintf("[INSTALL] Adding user '%s' to system database...", username), 150)
	logLine(fmt.Sprintf("[INSTALL] Created home directory: /home/%s", username), 200)
	logLine(fmt.Sprintf("[INSTALL] Generating default environment config for user..."), 100)
	
	logLine("[DOWNLOAD] Fetching LinuxGSM bootstrap installer...", 300)
	logLine("Connecting to linuxgsm.sh (linuxgsm.sh)... 172.67.200.75", 200)
	logLine("HTTP request sent, awaiting response... 200 OK", 250)
	logLine("Length: 26829 (26K) [application/x-sh]", 100)
	logLine("Saving to: '/home/" + username + "/linuxgsm.sh'", 150)
	logLine("100% [====================================>] 26,829      --.-KB/s   in 0.005s", 200)
	
	logLine(fmt.Sprintf("[INSTALL] Running: runuser -l %s -c \"./linuxgsm.sh %s\"", username, gameCmd), 300)
	logLine("Initializing script variables...", 200)
	logLine("Installing dependencies check...", 250)
	logLine("All required dependency packages are already installed.", 100)
	logLine(fmt.Sprintf("Created executable script: /home/%s/%s", username, gameCmd), 200)
	
	logLine(fmt.Sprintf("[INSTALL] Running: runuser -l %s -c \"./%s auto-install\"", username, gameCmd), 400)
	logLine("SteamCMD is already installed in /home/" + username + "/.steam", 200)
	logLine("Logging into Steam anonymously...", 300)
	logLine("Checking for app update (AppID: 896660)...", 200)
	logLine("App '896660' state is 0x11 after update job.", 300)
	
	// Steam download progress emulation
	for i := 10; i <= 100; i += 15 {
		if i > 100 {
			i = 100
		}
		progressBar := ""
		for j := 0; j < 30; j++ {
			if j < (i * 30 / 100) {
				progressBar += "="
			} else if j == (i * 30 / 100) {
				progressBar += ">"
			} else {
				progressBar += " "
			}
		}
		logLine(fmt.Sprintf("SteamCMD Downloading: [%s] %d%%", progressBar, i), 350)
	}

	logLine("SteamCMD Download complete.", 150)
	logLine("Verifying file signatures...", 200)
	logLine("Applying initial configurations...", 200)
	logLine("Creating default config: /home/" + username + "/lgsm/config-lgsm/" + gameCmd + "/common.cfg", 300)
	logLine("Creating default config: /home/" + username + "/lgsm/config-lgsm/" + gameCmd + "/" + gameCmd + ".cfg", 100)
	logLine("[INSTALL] Installation completed successfully!", 300)

	// Callback to save server to in-memory list
	gameName := gameCmd
	if gameCmd == "vhserver" {
		gameName = "Valheim"
	} else if gameCmd == "arkserver" {
		gameName = "Ark: Survival Evolved"
	} else {
		gameName = fmt.Sprintf("LGSM: %s", gameCmd)
	}

	addServerCallback(GameServerInstance{
		ID:     username,
		Name:   gameName,
		User:   username,
		Script: gameCmd,
		Status: "stopped",
		Port:   2456,
		Game:   gameCmd,
	})

	sendSSE("message", map[string]interface{}{
		"type": "exit",
		"code": 0,
	})
}

// StreamMockRestore writes a sequence of simulated console events to the client SSE stream during a backup restore
func StreamMockRestore(w http.ResponseWriter, r *http.Request, serverID, filename string, updateStatusCallback func(string)) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sendSSE := func(eventType string, data interface{}) {
		jsonData, _ := json.Marshal(data)
		fmt.Fprintf(w, "data: %s\n\n", jsonData)
		flusher.Flush()
	}

	logLine := func(text string, delayMs int) {
		time.Sleep(time.Duration(delayMs) * time.Millisecond)
		sendSSE("message", map[string]interface{}{
			"type": "log",
			"text": text,
		})
	}

	logLine(fmt.Sprintf("[%s] Starting restore of backup %s for %s...", time.Now().Format("15:04:05"), filename, serverID), 100)
	logLine("Stopping game server for safe restoration...", 200)
	updateStatusCallback("stopped")
	logLine("Stopping tmux session... OK", 150)
	logLine(fmt.Sprintf("Extracting backup archive %s...", filename), 300)
	logLine("tar -xzf archive: [=====>                         ] 18%", 200)
	logLine("tar -xzf archive: [===========>                   ] 38%", 200)
	logLine("tar -xzf archive: [===================>           ] 65%", 200)
	logLine("tar -xzf archive: [=========================>     ] 85%", 200)
	logLine("tar -xzf archive: [==============================>] 100%", 200)
	logLine("Restoring configurations... OK", 200)
	logLine("Verifying file permissions... OK", 150)
	logLine("Starting server back up...", 300)
	updateStatusCallback("running")
	logLine("Server is now running.", 100)
	logLine(fmt.Sprintf("[%s] Restore completed successfully.", time.Now().Format("15:04:05")), 100)

	sendSSE("message", map[string]interface{}{
		"type": "exit",
		"code": 0,
	})
}
