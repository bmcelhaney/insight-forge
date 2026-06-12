package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Env                    string
	Port                   int
	DuckDBPath             string
	BasePath               string // e.g. "/insightforge" — for running under a subpath on the sprite
	LogLevel               slog.Level
	PartsBaseEnabled       bool
	PartsBaseClientID      string
	PartsBaseClientSecret  string
	PartsBaseUsername      string
	PartsBasePassword      string
	PartsBaseAuthURL       string
	PartsBaseBaseURL       string
	PartsBaseGovDataPath   string
	PartsBaseGovDataType   string
	PartsBaseGovDataStart  string
	PartsBaseGovDataSections []string
	PartsBaseOAuthGrantType string
	PartsBaseOAuthScope    string
	PartsBaseTimeoutSeconds int
}

func Load() (*Config, error) {
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

	partsBaseClientID := getConfiguredValue("PARTSBASE_CLIENT_ID")
	partsBaseClientSecret := getConfiguredValue("PARTSBASE_CLIENT_SECRET")
	partsBaseUsername := getConfiguredValue("PARTSBASE_USERNAME")
	partsBasePassword := getConfiguredValue("PARTSBASE_PASSWORD")

	cfg := &Config{
		Env:                    viper.GetString("ENV"),
		Port:                   viper.GetInt("PORT"),
		DuckDBPath:             viper.GetString("DUCKDB_PATH"),
		BasePath:               viper.GetString("BASE_PATH"),
		PartsBaseEnabled:       viper.GetBool("PARTSBASE_ENABLED"),
		PartsBaseClientID:      partsBaseClientID,
		PartsBaseClientSecret:  partsBaseClientSecret,
		PartsBaseUsername:      partsBaseUsername,
		PartsBasePassword:      partsBasePassword,
		PartsBaseAuthURL:       strings.TrimSpace(viper.GetString("PARTSBASE_AUTH_URL")),
		PartsBaseBaseURL:       strings.TrimSpace(viper.GetString("PARTSBASE_BASE_URL")),
		PartsBaseGovDataPath:   strings.TrimSpace(viper.GetString("PARTSBASE_GOVDATA_PATH")),
		PartsBaseGovDataType:   strings.TrimSpace(viper.GetString("PARTSBASE_GOVDATA_TYPE")),
		PartsBaseGovDataStart:  strings.TrimSpace(viper.GetString("PARTSBASE_GOVDATA_START_DATE")),
		PartsBaseGovDataSections: parseCSV(viper.GetString("PARTSBASE_GOVDATA_SECTIONS")),
		PartsBaseOAuthGrantType: strings.TrimSpace(viper.GetString("PARTSBASE_OAUTH_GRANT_TYPE")),
		PartsBaseOAuthScope:    strings.TrimSpace(viper.GetString("PARTSBASE_OAUTH_SCOPE")),
		PartsBaseTimeoutSeconds: viper.GetInt("PARTSBASE_TIMEOUT_SECONDS"),
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

func getConfiguredValue(key string) string {
	v := strings.TrimSpace(viper.GetString(key))
	if v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(key))
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
