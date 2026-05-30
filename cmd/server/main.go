package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/components"
	"github.com/bmcelhaney/insight-forge/internal/config"
	"github.com/bmcelhaney/insight-forge/internal/db"
	"github.com/bmcelhaney/insight-forge/internal/export"
	"github.com/bmcelhaney/insight-forge/internal/extraction"
	"github.com/bmcelhaney/insight-forge/internal/models"
	"github.com/bmcelhaney/insight-forge/internal/processing"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	datastar "github.com/starfederation/datastar-go/datastar"
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

	database, err := db.New(cfg.DuckDBPath)
	if err != nil {
		slog.Error("failed to open DuckDB", "path", cfg.DuckDBPath, "error", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := database.Migrate(context.Background()); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}

	// Core intelligence components
	extractorReg := extraction.NewDefaultRegistry()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(120 * time.Second))

	// Health
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "service": "insight-forge"})
	})

	// === Main Workspace - initial HTML load ===
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		recent, _ := database.GetRecentAnalyses(r.Context(), 8)
		props := components.WorkspaceProps{
			RecentNSNs: recent,
		}
		html, _ := components.RenderWorkspaceToString(props)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, html)
	})

	// === Datastar SSE endpoint for reactive analysis (with live partial updates) ===
	r.Post("/datastar/analyze", func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)

		nsn := r.FormValue("nsn")
		if nsn == "" {
			var req struct{ NSN string `json:"nsn"` }
			json.NewDecoder(r.Body).Decode(&req)
			nsn = req.NSN
		}
		if nsn == "" {
			sse.PatchElements(`<div class="alert alert-error">NSN is required</div>`)
			return
		}

		ctx := r.Context()

		// Known sources for this prototype (order matters for nice progress UX)
		sources := []string{"WEBFLIS", "FPDS", "SANCTIONS"}
		totalSources := len(sources)

		// Initial analyzing state
		recent, _ := database.GetRecentAnalyses(ctx, 8)
		analyzingProps := components.WorkspaceProps{
			NSN:              nsn,
			RecentNSNs:       recent,
			IsAnalyzing:      true,
			CompletedSources: []string{},
			TotalSources:     totalSources,
		}
		analyzingHTML, _ := components.RenderWorkspaceToString(analyzingProps)
		sse.PatchElements(analyzingHTML)

		var accumulatedSnaps []models.DataSnapshot
		var completed []string

		for _, source := range sources {
			// Fetch this source
			snaps, err := extractorReg.FetchAll(ctx, nsn, []string{source}, nil)
			if err == nil && len(snaps) > 0 {
				accumulatedSnaps = append(accumulatedSnaps, snaps...)
			}
			completed = append(completed, source)

			// Partial synthesis with what we have so far
			partialResult, _ := processing.Synthesize(ctx, nsn, accumulatedSnaps)

			// Persist partial (last one will be the final)
			_ = database.StoreSnapshots(ctx, snaps)
			_ = database.StoreResult(ctx, partialResult)

			// Live patch to the UI
			partialRecent, _ := database.GetRecentAnalyses(ctx, 8)
			partialProps := components.WorkspaceProps{
				NSN:              nsn,
				Result:           &partialResult,
				Snapshots:        accumulatedSnaps,
				RecentNSNs:       partialRecent,
				IsAnalyzing:      true, // still "analyzing" until the very last
				CompletedSources: append([]string{}, completed...),
				TotalSources:     totalSources,
			}
			partialHTML, _ := components.RenderWorkspaceToString(partialProps)
			sse.PatchElements(partialHTML)
		}

		// Final state - mark as complete
		finalRecent, _ := database.GetRecentAnalyses(ctx, 8)
		finalResult, _ := processing.Synthesize(ctx, nsn, accumulatedSnaps)
		_ = database.StoreResult(ctx, finalResult)

		finalProps := components.WorkspaceProps{
			NSN:        nsn,
			Result:     &finalResult,
			Snapshots:  accumulatedSnaps,
			RecentNSNs: finalRecent,
		}
		finalHTML, _ := components.RenderWorkspaceToString(finalProps)
		sse.PatchElements(finalHTML)
	})

	// Simple error display helper for Datastar
	r.Get("/datastar/error", func(w http.ResponseWriter, r *http.Request) {
		msg := r.URL.Query().Get("msg")
		if msg == "" {
			msg = "Something went wrong during analysis."
		}
		html := fmt.Sprintf(`<div class="alert alert-error shadow-lg"><span>%s</span></div>`, msg)
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	})

	// === Analyze / Ingest endpoint (JSON API for tools) ===
	r.Post("/api/analyze", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			NSN     string   `json:"nsn"`
			Sources []string `json:"sources,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NSN == "" {
			http.Error(w, "invalid request - nsn required", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()

		// 1. Run extractors in parallel
		snaps, err := extractorReg.FetchAll(ctx, req.NSN, req.Sources, nil)
		if err != nil {
			slog.Error("extraction failed", "nsn", req.NSN, "error", err)
		}

		// 2. Synthesize
		result, err := processing.Synthesize(ctx, req.NSN, snaps)
		if err != nil {
			http.Error(w, "synthesis failed", http.StatusInternalServerError)
			return
		}

		// 3. Persist (simplified for prototype)
		_ = database.StoreSnapshots(ctx, snaps)
		_ = database.StoreResult(ctx, result)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"nsn":    req.NSN,
			"result": result,
			"sources_used": len(snaps),
		})
	})

	// === Get latest result for an NSN ===
	r.Get("/api/result/{nsn}", func(w http.ResponseWriter, r *http.Request) {
		nsn := chi.URLParam(r, "nsn")
		result, err := database.GetLatestResult(r.Context(), nsn)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})

	// === Export structured payload for pricing tool (JSON) ===
	r.Get("/api/export/{nsn}", func(w http.ResponseWriter, r *http.Request) {
		nsn := chi.URLParam(r, "nsn")
		result, _ := database.GetLatestResult(r.Context(), nsn)
		snaps, _ := database.GetSnapshots(r.Context(), nsn)

		payload := map[string]any{
			"nsn":            nsn,
			"generated_at":   time.Now(),
			"insight":        result,
			"snapshots":      snaps,
			"export_version": "1.0",
			"note":           "Prototype payload for fair-market pricing tool integration",
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="insight-forge-%s.json"`, nsn))
		json.NewEncoder(w).Encode(payload)
	})

	// === Export full evidence bundle as Excel (.xlsx) ===
	r.Get("/api/export-excel/{nsn}", func(w http.ResponseWriter, r *http.Request) {
		nsn := chi.URLParam(r, "nsn")

		f, err := export.GenerateExcelBundle(r.Context(), database, nsn)
		if err != nil {
			http.Error(w, "failed to generate excel", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="insight-forge-%s.xlsx"`, nsn))

		if err := f.Write(w); err != nil {
			slog.Error("failed to write excel", "error", err)
		}
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

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}


