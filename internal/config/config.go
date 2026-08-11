package config

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Env                      string
	Port                     int
	DuckDBPath               string
	BasePath                 string // e.g. "/insightforge" — for running under a subpath on the sprite
	LogLevel                 slog.Level
	PartsBaseEnabled         bool
	PartsBaseConfigured      bool // true when all OAuth credentials are present
	PartsBaseEnvFilesLoaded  []string
	PartsBaseClientID        string
	PartsBaseClientSecret    string
	PartsBaseUsername        string
	PartsBasePassword        string
	PartsBaseAuthURL         string
	PartsBaseBaseURL         string
	PartsBaseGovDataPath     string
	PartsBaseGovDataType     string
	PartsBaseGovDataStart    string
	PartsBaseGovDataSections []string
	PartsBaseOAuthGrantType  string
	PartsBaseOAuthScope      string
	PartsBaseTimeoutSeconds  int
	// SerpAPI (Google Shopping) for commercial product prices/links
	SerpAPIEnabled    bool
	SerpAPIConfigured bool
	SerpAPIKey        string
	SerpAPINum        int // shopping results to request (top N)
	// SerpAPIImmersive enables google_immersive_product follow-up (extra quota).
	// Set IF_SERPAPI_IMMERSIVE=false to restore shopping-search-only behavior.
	SerpAPIImmersive bool
	// UPCItemDB paid plan (DEV/PRO) for product identity + market offers
	UPCItemDBEnabled    bool
	UPCItemDBConfigured bool
	UPCItemDBKey        string
	// Tigris (S3) for pricing-evidence screenshots
	TigrisEnabled   bool
	TigrisBucket    string
	TigrisEndpoint  string
	TigrisRegion    string
	TigrisAccessKey string
	TigrisSecretKey string
	// Screenshots of links.url → Tigris
	ScreenshotEnabled    bool
	ScreenshotMaxPerRun  int
	ScreenshotTimeoutMS  int
	ScreenshotOnAnalyze  bool // if true, every analyze captures (slow); else request flag only
}

func Load() (*Config, error) {
	// Load optional dotenv files before Viper reads the environment.
	// Real process env always wins (we never override existing keys).
	loadedFiles := loadOptionalEnvFiles(
		".env",
		".env.partsbase",
		".env.serpapi",
		".env.upcitemdb",
		".env.tigris",
		".env.local",
	)

	viper.SetEnvPrefix("IF")
	viper.AutomaticEnv()

	viper.SetDefault("ENV", "development")
	viper.SetDefault("PORT", 8080)
	viper.SetDefault("DUCKDB_PATH", "./data/insight-forge.duckdb")
	viper.SetDefault("BASE_PATH", "")
	viper.SetDefault("LOG_LEVEL", "info")
	viper.SetDefault("PARTSBASE_ENABLED", true)
	viper.SetDefault("PARTSBASE_AUTH_URL", "https://auth.partsbase.com/connect/token")
	viper.SetDefault("PARTSBASE_BASE_URL", "https://apiservices.partsbase.com")
	viper.SetDefault("PARTSBASE_GOVDATA_PATH", "/api/data/GovData")
	viper.SetDefault("PARTSBASE_GOVDATA_TYPE", "Nsn")
	viper.SetDefault("PARTSBASE_GOVDATA_START_DATE", "2000-01-01")
	viper.SetDefault("PARTSBASE_GOVDATA_SECTIONS", "Procurement,NsnId")
	viper.SetDefault("PARTSBASE_OAUTH_GRANT_TYPE", "password")
	viper.SetDefault("PARTSBASE_OAUTH_SCOPE", "api")
	viper.SetDefault("PARTSBASE_TIMEOUT_SECONDS", 30)
	viper.SetDefault("SERPAPI_ENABLED", true)
	viper.SetDefault("SERPAPI_NUM", 8)
	// P2: Immersive Product multi-store enrichment (uses extra SerpAPI credits).
	// Default on for richer merchant prices; set false to burn less quota.
	viper.SetDefault("SERPAPI_IMMERSIVE", true)
	viper.SetDefault("UPCITEMDB_ENABLED", true)
	viper.SetDefault("TIGRIS_ENABLED", false)
	viper.SetDefault("TIGRIS_REGION", "auto")
	viper.SetDefault("SCREENSHOT_ENABLED", false)
	viper.SetDefault("SCREENSHOT_MAX_PER_RUN", 15)
	viper.SetDefault("SCREENSHOT_TIMEOUT_MS", 30000)
	// Default true: every analyze/export captures evidence when Tigris is configured.
	// Opt out per request with capture_screenshots=false, or set IF_SCREENSHOT_ON_ANALYZE=false.
	viper.SetDefault("SCREENSHOT_ON_ANALYZE", true)

	partsBaseClientID := getConfiguredValue("PARTSBASE_CLIENT_ID")
	partsBaseClientSecret := getConfiguredValue("PARTSBASE_CLIENT_SECRET")
	partsBaseUsername := getConfiguredValue("PARTSBASE_USERNAME")
	partsBasePassword := getConfiguredValue("PARTSBASE_PASSWORD")
	partsBaseConfigured := partsBaseClientID != "" &&
		partsBaseClientSecret != "" &&
		partsBaseUsername != "" &&
		partsBasePassword != ""

	serpKey := getConfiguredValue("SERPAPI_KEY")
	if serpKey == "" {
		// Also accept unprefixed SERPAPI_KEY from dotenv files.
		serpKey = strings.TrimSpace(os.Getenv("SERPAPI_KEY"))
	}
	serpNum := viper.GetInt("SERPAPI_NUM")
	if serpNum <= 0 {
		serpNum = 8
	}
	if serpNum > 20 {
		serpNum = 20
	}

	upcKey := getConfiguredValue("UPCITEMDB_KEY")
	if upcKey == "" {
		upcKey = getConfiguredValue("UPCITEMDB_USER_KEY")
	}
	if upcKey == "" {
		upcKey = strings.TrimSpace(os.Getenv("UPCITEMDB_KEY"))
	}
	if upcKey == "" {
		upcKey = strings.TrimSpace(os.Getenv("UPCITEMDB_USER_KEY"))
	}

	// Tigris: prefer IF_* then standard AWS_* names from Tigris console.
	tigrisBucket := firstNonEmpty(
		getConfiguredValue("TIGRIS_BUCKET"),
		strings.TrimSpace(os.Getenv("BUCKET_NAME")),
		strings.TrimSpace(os.Getenv("AWS_S3_BUCKET")),
	)
	tigrisEndpoint := firstNonEmpty(
		getConfiguredValue("TIGRIS_ENDPOINT"),
		strings.TrimSpace(os.Getenv("AWS_ENDPOINT_URL_S3")),
	)
	tigrisRegion := firstNonEmpty(
		getConfiguredValue("TIGRIS_REGION"),
		strings.TrimSpace(os.Getenv("AWS_REGION")),
		"auto",
	)
	tigrisAK := firstNonEmpty(
		getConfiguredValue("TIGRIS_ACCESS_KEY"),
		getConfiguredValue("AWS_ACCESS_KEY_ID"),
		strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")),
	)
	tigrisSK := firstNonEmpty(
		getConfiguredValue("TIGRIS_SECRET_KEY"),
		getConfiguredValue("AWS_SECRET_ACCESS_KEY"),
		strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY")),
	)
	tigrisEnabled := viper.GetBool("TIGRIS_ENABLED")
	if !tigrisEnabled && tigrisBucket != "" && tigrisEndpoint != "" && tigrisAK != "" && tigrisSK != "" {
		// Auto-enable when full credentials present (e.g. .env.tigris).
		tigrisEnabled = true
	}

	shotMax := viper.GetInt("SCREENSHOT_MAX_PER_RUN")
	if shotMax <= 0 {
		shotMax = 15
	}
	if shotMax > 40 {
		shotMax = 40
	}
	shotTO := viper.GetInt("SCREENSHOT_TIMEOUT_MS")
	if shotTO <= 0 {
		shotTO = 30000
	}

	cfg := &Config{
		Env:                      viper.GetString("ENV"),
		Port:                     viper.GetInt("PORT"),
		DuckDBPath:               viper.GetString("DUCKDB_PATH"),
		BasePath:                 viper.GetString("BASE_PATH"),
		PartsBaseEnabled:         viper.GetBool("PARTSBASE_ENABLED"),
		PartsBaseConfigured:      partsBaseConfigured,
		PartsBaseEnvFilesLoaded:  loadedFiles,
		PartsBaseClientID:        partsBaseClientID,
		PartsBaseClientSecret:    partsBaseClientSecret,
		PartsBaseUsername:        partsBaseUsername,
		PartsBasePassword:        partsBasePassword,
		PartsBaseAuthURL:         strings.TrimSpace(viper.GetString("PARTSBASE_AUTH_URL")),
		PartsBaseBaseURL:         strings.TrimSpace(viper.GetString("PARTSBASE_BASE_URL")),
		PartsBaseGovDataPath:     strings.TrimSpace(viper.GetString("PARTSBASE_GOVDATA_PATH")),
		PartsBaseGovDataType:     strings.TrimSpace(viper.GetString("PARTSBASE_GOVDATA_TYPE")),
		PartsBaseGovDataStart:    strings.TrimSpace(viper.GetString("PARTSBASE_GOVDATA_START_DATE")),
		PartsBaseGovDataSections: parseCSV(viper.GetString("PARTSBASE_GOVDATA_SECTIONS")),
		PartsBaseOAuthGrantType:  strings.TrimSpace(viper.GetString("PARTSBASE_OAUTH_GRANT_TYPE")),
		PartsBaseOAuthScope:      strings.TrimSpace(viper.GetString("PARTSBASE_OAUTH_SCOPE")),
		PartsBaseTimeoutSeconds:  viper.GetInt("PARTSBASE_TIMEOUT_SECONDS"),
		SerpAPIEnabled:           viper.GetBool("SERPAPI_ENABLED"),
		SerpAPIConfigured:        serpKey != "",
		SerpAPIKey:               serpKey,
		SerpAPINum:               serpNum,
		SerpAPIImmersive:         viper.GetBool("SERPAPI_IMMERSIVE"),
		UPCItemDBEnabled:         viper.GetBool("UPCITEMDB_ENABLED"),
		UPCItemDBConfigured:      upcKey != "",
		UPCItemDBKey:             upcKey,
		TigrisEnabled:            tigrisEnabled,
		TigrisBucket:             tigrisBucket,
		TigrisEndpoint:           tigrisEndpoint,
		TigrisRegion:             tigrisRegion,
		TigrisAccessKey:          tigrisAK,
		TigrisSecretKey:          tigrisSK,
		ScreenshotEnabled:        viper.GetBool("SCREENSHOT_ENABLED"),
		ScreenshotMaxPerRun:      shotMax,
		ScreenshotTimeoutMS:      shotTO,
		ScreenshotOnAnalyze:      viper.GetBool("SCREENSHOT_ON_ANALYZE"),
	}

	levelStr := viper.GetString("LOG_LEVEL")
	switch levelStr {
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "warn":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	default:
		cfg.LogLevel = slog.LevelInfo
	}

	if err := viper.BindEnv("DUCKDB_PATH"); err != nil {
		return nil, fmt.Errorf("bind env: %w", err)
	}

	return cfg, nil
}

// loadOptionalEnvFiles loads KEY=VALUE pairs from dotenv-style files if present.
// Existing process environment variables are never overwritten.
// Returns the list of files that were successfully read (not necessarily ones that set keys).
// Searches CWD, executable directory, and common sprite paths so deploys that start
// the binary outside the repo root still pick up .env.partsbase.
func loadOptionalEnvFiles(names ...string) []string {
	var loaded []string
	seen := map[string]bool{}
	for _, name := range names {
		for _, path := range resolveEnvFileCandidates(name) {
			if seen[path] {
				continue
			}
			seen[path] = true
			n, err := loadEnvFile(path)
			if err != nil {
				if !os.IsNotExist(err) {
					fmt.Fprintf(os.Stderr, "config: skip env file %s: %v\n", path, err)
				}
				continue
			}
			if n >= 0 {
				loaded = append(loaded, path)
			}
		}
	}
	return loaded
}

func resolveEnvFileCandidates(name string) []string {
	if filepath.IsAbs(name) {
		return []string{name}
	}
	var out []string
	// 1) Current working directory (reset.sh / local dev).
	out = append(out, name)
	// 2) Directory of the running executable (nohup ./insight-forge from any cwd).
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		out = append(out, filepath.Join(filepath.Dir(exe), name))
	}
	// 3) Common sprite deploy locations.
	out = append(out,
		filepath.Join("/home/sprite/insight-forge", name),
		filepath.Join("/home/sprite", name),
	)
	return out
}

// loadEnvFile parses a simple dotenv file and sets unset environment variables.
// Supports: comments (#), blank lines, optional export prefix, single/double quotes.
func loadEnvFile(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return -1, err
	}
	defer f.Close()

	setCount := 0
	scanner := bufio.NewScanner(f)
	// Allow long secret lines.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		// Inline comment only when unquoted.
		if i := indexUnquoted(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if key == "" {
			continue
		}
		val = unquoteEnvValue(val)
		// Never clobber process environment (sprite/systemd overrides win).
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return setCount, fmt.Errorf("setenv %s (line %d): %w", key, lineNo, err)
		}
		setCount++
	}
	if err := scanner.Err(); err != nil {
		return setCount, err
	}
	return setCount, nil
}

func unquoteEnvValue(val string) string {
	if len(val) >= 2 {
		if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
			return val[1 : len(val)-1]
		}
	}
	return val
}

func indexUnquoted(s string, ch byte) int {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		default:
			if c == ch && !inSingle && !inDouble {
				return i
			}
		}
	}
	return -1
}

func getConfiguredValue(key string) string {
	v := strings.TrimSpace(viper.GetString(key))
	if v != "" {
		return v
	}
	// Prefer IF_-prefixed env (matches Viper prefix), then bare key.
	if v := strings.TrimSpace(os.Getenv("IF_" + key)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(key))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func parseCSV(v string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(v, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		out = append(out, trimmed)
	}
	return out
}
