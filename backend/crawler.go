package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type GameCrawlItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Cmd  string `json:"cmd"`
	Icon string `json:"icon"`
}

type GameCrawler struct {
	mu        sync.Mutex
	status    string // idle | syncing
	progress  string
	lastError string
	games     []GameCrawlItem
	cachePath string
	iconsDir  string
}

func NewGameCrawler(configDir string) *GameCrawler {
	cachePath := filepath.Join(configDir, "games_cache.json")
	iconsDir := filepath.Join(configDir, "icons")
	
	crawler := &GameCrawler{
		status:    "idle",
		cachePath: cachePath,
		iconsDir:  iconsDir,
	}
	
	// Load cached games on startup if file exists
	crawler.LoadCache()
	return crawler
}

func (gc *GameCrawler) LoadCache() {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	
	file, err := os.Open(gc.cachePath)
	if err != nil {
		return
	}
	defer file.Close()
	
	var games []GameCrawlItem
	if err := json.NewDecoder(file).Decode(&games); err == nil {
		gc.games = games
	}
}

func (gc *GameCrawler) GetStatus() (string, string, string) {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	return gc.status, gc.progress, gc.lastError
}

var popularFallbackGames = []GameCrawlItem{
	{ID: "ark", Name: "Ark: Survival Evolved", Cmd: "arkserver", Icon: "/api/games/icon/arkserver"},
	{ID: "valheim", Name: "Valheim", Cmd: "vhserver", Icon: "/api/games/icon/vhserver"},
	{ID: "cs2", Name: "Counter-Strike 2", Cmd: "cs2server", Icon: "/api/games/icon/cs2server"},
	{ID: "rust", Name: "Rust", Cmd: "rustserver", Icon: "/api/games/icon/rustserver"},
	{ID: "minecraft", Name: "Minecraft", Cmd: "mcserver", Icon: "/api/games/icon/mcserver"},
	{ID: "sdtd", Name: "7 Days to Die", Cmd: "sdtdserver", Icon: "/api/games/icon/sdtdserver"},
	{ID: "gmod", Name: "Garry's Mod", Cmd: "gmodserver", Icon: "/api/games/icon/gmodserver"},
	{ID: "terraria", Name: "Terraria", Cmd: "tserver", Icon: "/api/games/icon/tserver"},
	{ID: "factorio", Name: "Factorio", Cmd: "fctrserver", Icon: "/api/games/icon/fctrserver"},
	{ID: "palworld", Name: "Palworld", Cmd: "pwserver", Icon: "/api/games/icon/pwserver"},
	{ID: "projectzomboid", Name: "Project Zomboid", Cmd: "pzserver", Icon: "/api/games/icon/pzserver"},
}

func (gc *GameCrawler) GetGames() []GameCrawlItem {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if len(gc.games) == 0 {
		return popularFallbackGames
	}
	return gc.games
}

func (gc *GameCrawler) StartSync() {
	gc.mu.Lock()
	if gc.status == "syncing" {
		gc.mu.Unlock()
		return
	}
	gc.status = "syncing"
	gc.progress = "Starting fetch..."
	gc.lastError = ""
	gc.mu.Unlock()
	
	go gc.runSync()
}

func (gc *GameCrawler) runSync() {
	defer func() {
		gc.mu.Lock()
		gc.status = "idle"
		gc.mu.Unlock()
	}()
	
	// Create icons directory if missing
	os.MkdirAll(gc.iconsDir, 0755)
	
	// 1. Fetch HTML of linuxgsm.com/servers/
	resp, err := http.Get("https://linuxgsm.com/servers/")
	if err != nil {
		gc.mu.Lock()
		gc.lastError = "Failed to fetch LinuxGSM page: " + err.Error()
		gc.mu.Unlock()
		return
	}
	defer resp.Body.Close()
	
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		gc.mu.Lock()
		gc.lastError = "Failed to read response body: " + err.Error()
		gc.mu.Unlock()
		return
	}
	
	html := string(bodyBytes)
	
	// 2. Parse anchor cards matching server detail pages
	// Pattern: <a ... href=".../servers/([a-z0-9\-]+server)/?" ... > (content) </a>
	anchorRegex := regexp.MustCompile(`<a[^>]*href="[^"]*/servers/([a-z0-9\-]+server)/?"[^>]*>([\s\S]*?)</a>`)
	matches := anchorRegex.FindAllStringSubmatch(html, -1)
	
	if len(matches) == 0 {
		gc.mu.Lock()
		gc.lastError = "No gameserver cards found in HTML. Check page selector."
		gc.mu.Unlock()
		return
	}
	
	imgRegex := regexp.MustCompile(`src="([^"]+)"`)
	tagRegex := regexp.MustCompile(`<[^>]*>`)
	
	var crawledGames []GameCrawlItem
	seenGames := make(map[string]int) // maps scriptName -> index in crawledGames
	
	gc.mu.Lock()
	gc.progress = fmt.Sprintf("Found %d raw HTML elements, scanning for games...", len(matches))
	gc.mu.Unlock()
	
	for _, match := range matches {
		scriptName := match[1]
		innerContent := match[2]
		
		// Extract Image URL
		imgSrc := ""
		imgMatch := imgRegex.FindStringSubmatch(innerContent)
		if len(imgMatch) > 1 {
			imgSrc = imgMatch[1]
		}
		
		// Clean Game Name
		cleanName := tagRegex.ReplaceAllString(innerContent, " ")
		cleanName = strings.Join(strings.Fields(cleanName), " ") // collapse spaces
		
		if len(cleanName) < 2 {
			cleanName = ""
		}
		
		existingIdx, seen := seenGames[scriptName]
		if seen {
			// Deduplication: if we've seen this command, we update it if we found an icon now
			if imgSrc != "" && crawledGames[existingIdx].Icon == "" {
				ext := ".png"
				if strings.Contains(imgSrc, ".jpg") || strings.Contains(imgSrc, ".jpeg") {
					ext = ".jpg"
				} else if strings.Contains(imgSrc, ".webp") {
					ext = ".webp"
				}
				
				destPath := filepath.Join(gc.iconsDir, scriptName+ext)
				if _, statErr := os.Stat(destPath); statErr != nil {
					gc.downloadFile(imgSrc, destPath)
				}
				
				crawledGames[existingIdx].Icon = "/api/games/icon/" + scriptName
			}
			
			// Update name if we found a better/longer name
			if cleanName != "" && len(crawledGames[existingIdx].Name) < len(cleanName) {
				crawledGames[existingIdx].Name = cleanName
			}
			continue
		}
		
		// New game entry
		if cleanName == "" {
			cleanName = strings.TrimSuffix(scriptName, "server")
			cleanName = strings.ToUpper(cleanName[0:1]) + cleanName[1:]
		}
		
		// Download and cache icon
		hasIcon := false
		if imgSrc != "" {
			ext := ".png"
			if strings.Contains(imgSrc, ".jpg") || strings.Contains(imgSrc, ".jpeg") {
				ext = ".jpg"
			} else if strings.Contains(imgSrc, ".webp") {
				ext = ".webp"
			}
			
			destPath := filepath.Join(gc.iconsDir, scriptName+ext)
			if _, statErr := os.Stat(destPath); statErr != nil {
				gc.downloadFile(imgSrc, destPath)
			}
			hasIcon = true
		}
		
		iconPath := ""
		if hasIcon {
			iconPath = "/api/games/icon/" + scriptName
		}
		
		gameID := strings.TrimSuffix(scriptName, "server")
		
		crawledGames = append(crawledGames, GameCrawlItem{
			ID:   gameID,
			Name: cleanName,
			Cmd:  scriptName,
			Icon: iconPath,
		})
		
		seenGames[scriptName] = len(crawledGames) - 1
		
		gc.mu.Lock()
		gc.progress = fmt.Sprintf("Processing games (%d discovered so far)...", len(crawledGames))
		gc.mu.Unlock()
	}
	
	// 3. Save cache
	cacheFile, err := os.Create(gc.cachePath)
	if err == nil {
		json.NewEncoder(cacheFile).Encode(crawledGames)
		cacheFile.Close()
	}
	
	gc.mu.Lock()
	gc.games = crawledGames
	gc.progress = fmt.Sprintf("Successfully synced %d servers!", len(crawledGames))
	gc.mu.Unlock()
}

func (gc *GameCrawler) downloadFile(url string, destPath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()
	
	_, err = io.Copy(out, resp.Body)
	return err
}
