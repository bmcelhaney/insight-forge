package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Env                       string
	Port                      int
	DuckDBPath                string
	BasePath                  string // e.g. "/insightforge" — for running under a subpath on the sprite
	LogLevel                  slog.Level
	PartsBaseEnabled          bool
	PartsBaseAPIKey           string
	PartsBaseBaseURL          string
	PartsBaseMarketPricingPath string
	PartsBaseTimeoutSeconds   int
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
	viper.SetDefault("PARTSBASE_BASE_URL", "https://services.partsbase.com")
	viper.SetDefault("PARTSBASE_MARKET_PRICING_PATH", "/api-market-pricing")
	viper.SetDefault("PARTSBASE_TIMEOUT_SECONDS", 10)

	partsBaseAPIKey := strings.TrimSpace(viper.GetString("PARTSBASE_API_KEY"))
	if partsBaseAPIKey == "" {
		partsBaseAPIKey = strings.TrimSpace(os.Getenv("PARTSBASE_API_KEY"))
	}

	cfg := &Config{
		Env:                        viper.GetString("ENV"),
		Port:                       viper.GetInt("PORT"),
		DuckDBPath:                 viper.GetString("DUCKDB_PATH"),
		BasePath:                   viper.GetString("BASE_PATH"),
		PartsBaseEnabled:           viper.GetBool("PARTSBASE_ENABLED"),
		PartsBaseAPIKey:            partsBaseAPIKey,
		PartsBaseBaseURL:           strings.TrimSpace(viper.GetString("PARTSBASE_BASE_URL")),
		PartsBaseMarketPricingPath: strings.TrimSpace(viper.GetString("PARTSBASE_MARKET_PRICING_PATH")),
		PartsBaseTimeoutSeconds:    viper.GetInt("PARTSBASE_TIMEOUT_SECONDS"),
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
