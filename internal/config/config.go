package config

import (
	"fmt"
	"log/slog"

	"github.com/spf13/viper"
)

type Config struct {
	Env        string
	Port       int
	DuckDBPath string
	LogLevel   slog.Level
}

func Load() (*Config, error) {
	viper.SetEnvPrefix("IF")
	viper.AutomaticEnv()

	viper.SetDefault("ENV", "development")
	viper.SetDefault("PORT", 8080)
	viper.SetDefault("DUCKDB_PATH", "./data/insight-forge.duckdb")
	viper.SetDefault("LOG_LEVEL", "info")

	cfg := &Config{
		Env:        viper.GetString("ENV"),
		Port:       viper.GetInt("PORT"),
		DuckDBPath: viper.GetString("DUCKDB_PATH"),
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
