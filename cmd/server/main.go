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

	"github.com/bmcelhaney/insight-forge/internal/config"
	"github.com/bmcelhaney/insight-forge/internal/db"
	"github.com/bmcelhaney/insight-forge/internal/extraction"
	"github.com/bmcelhaney/insight-forge/internal/models"
	"github.com/bmcelhaney/insight-forge/internal/processing"
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

	// === Main Workspace (HTML + Datastar ready) ===
	r.Get("/", workspaceHandler)

	// === Analyze / Ingest endpoint (triggers full multi-extractor run + synthesis) ===
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

	// === Export structured payload for pricing tool ===
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

func workspaceHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>Insight Forge • NSN Intelligence</title>
  <script src="https://cdn.jsdelivr.net/gh/starfederation/datastar@latest/bundles/datastar.js"></script>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui@4/dist/full.min.css">
  <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="bg-base-200 min-h-screen">
  <div class="navbar bg-base-100 shadow-lg">
    <div class="flex-1 px-6">
      <span class="text-2xl font-bold tracking-tight">Insight Forge</span>
      <span class="ml-3 badge badge-primary badge-sm">Prototype</span>
    </div>
    <div class="px-6 text-sm opacity-70">Running on nib-sprite • Stitchify Go Framework</div>
  </div>

  <div class="max-w-screen-2xl mx-auto p-8">
    <!-- Search -->
    <div class="flex gap-3 mb-8">
      <input id="nsn-input" type="text" placeholder="Enter NSN (e.g. 1234567890123 or 7890123)" 
             class="input input-bordered input-lg flex-1 font-mono text-lg" 
             value="1234567890123"
             onkeydown="if(event.key==='Enter') analyze()"/>
      <button onclick="analyze()" class="btn btn-primary btn-lg px-12">Analyze</button>
    </div>

    <!-- Results Area (will be enhanced with full Datastar reactivity) -->
    <div id="results" class="hidden">
      <div class="grid grid-cols-1 lg:grid-cols-12 gap-6">
        <!-- Left: Summary -->
        <div class="lg:col-span-5">
          <div class="card bg-base-100 shadow-xl">
            <div class="card-body">
              <h2 class="card-title">Executive Summary</h2>
              <div id="summary" class="prose"></div>
              
              <div class="stats shadow mt-4">
                <div class="stat">
                  <div class="stat-title">Viability</div>
                  <div id="viability" class="stat-value text-success"></div>
                </div>
                <div class="stat">
                  <div class="stat-title">Risk</div>
                  <div id="risk" class="stat-value text-warning"></div>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- Right: Key Flags + Export -->
        <div class="lg:col-span-7">
          <div class="card bg-base-100 shadow-xl">
            <div class="card-body">
              <div class="flex justify-between items-center">
                <h2 class="card-title">Risk Flags &amp; Actions</h2>
                <button onclick="exportJSON()" class="btn btn-outline btn-sm">Export JSON for Pricing Tool</button>
              </div>
              <div id="flags" class="flex flex-wrap gap-2 mt-3"></div>
            </div>
          </div>
        </div>
      </div>

      <div class="alert alert-success mt-6">
        <span>Full reactive Datastar workspace, go-echarts visualizations, supplier graphs, and live re-run coming in next iteration.</span>
      </div>
    </div>
  </div>

  <script>
    async function analyze() {
      const nsn = document.getElementById('nsn-input').value.trim();
      if (!nsn) return alert('Please enter an NSN');

      const res = await fetch('/api/analyze', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({ nsn })
      });
      
      if (!res.ok) {
        alert('Analysis failed');
        return;
      }
      
      const data = await res.json();
      renderResults(data);
    }

    function renderResults(data) {
      document.getElementById('results').classList.remove('hidden');
      
      const r = data.result;
      document.getElementById('summary').innerText = r.summary || 'Analysis complete.';
      document.getElementById('viability').innerText = (r.viability_score || 0).toFixed(0);
      document.getElementById('risk').innerText = (r.risk_score || 0).toFixed(0);

      const flagsEl = document.getElementById('flags');
      flagsEl.innerHTML = '';
      (r.flags || []).forEach(f => {
        const div = document.createElement('div');
        div.className = 'badge badge-warning gap-2';
        div.innerText = f.description;
        flagsEl.appendChild(div);
      });
    }

    async function exportJSON() {
      const nsn = document.getElementById('nsn-input').value.trim();
      if (!nsn) return;
      window.location = '/api/export/' + nsn;
    }

    // Auto-run example on load for demo convenience
    window.onload = () => {
      // Uncomment the next line if you want it to analyze automatically on load
      // setTimeout(() => { document.getElementById('nsn-input').value = '1234567890123'; analyze(); }, 400);
    }
  </script>
</body>
</html>`)
}
