package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/config"
	"github.com/bmcelhaney/insight-forge/internal/db"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	slog.SetDefault(logger)

	slog.Info("starting insight-forge", "env", cfg.Env, "port", cfg.Port)

	// Initialize DuckDB (single source of truth)
	database, err := db.New(cfg.DuckDBPath)
	if err != nil {
		slog.Error("failed to open DuckDB", "path", cfg.DuckDBPath, "error", err)
		os.Exit(1)
	}
	defer database.Close()

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	// Health
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"insight-forge"}`))
	})

	// Placeholder workspace (will become Datastar reactive endpoint)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>Insight Forge</title>
  <script src="https://cdn.jsdelivr.net/gh/starfederation/datastar@latest/bundles/datastar.js"></script>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui@4/dist/full.min.css">
  <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="bg-base-200">
  <div class="navbar bg-base-100 shadow">
    <div class="flex-1 px-4">
      <span class="text-xl font-bold">Insight Forge</span>
      <span class="ml-2 text-sm opacity-60">NSN Intelligence</span>
    </div>
  </div>

  <div class="p-8 max-w-screen-2xl mx-auto">
    <div class="alert alert-info mb-6">
      <span>Prototype running on nib-sprite architecture (Go + DuckDB + Datastar baseline from Stitchify framework).</span>
    </div>

    <h1 class="text-3xl font-bold mb-4">NSN Intelligence Workspace</h1>
    <p class="mb-6">Scaffolding in progress. Enter an NSN to begin analysis.</p>

    <div class="card bg-base-100 shadow-xl">
      <div class="card-body">
        <div class="flex gap-2">
          <input type="text" placeholder="Enter NSN (e.g. 1234567890123)" class="input input-bordered flex-1" />
          <button class="btn btn-primary">Analyze</button>
        </div>
        <div class="text-xs opacity-60 mt-2">Multi-extractor fan-out, viability/risk synthesis, and export coming next.</div>
      </div>
    </div>
  </div>
</body>
</html>`)
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	go func() {
		slog.Info("server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
