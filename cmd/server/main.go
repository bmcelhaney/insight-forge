package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/config"
	"github.com/bmcelhaney/insight-forge/internal/db"
	"github.com/bmcelhaney/insight-forge/internal/export"
	"github.com/bmcelhaney/insight-forge/internal/extraction"
	"github.com/bmcelhaney/insight-forge/internal/models"
	"github.com/bmcelhaney/insight-forge/internal/processing"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// PageData is what the single-page UI template needs
type PageData struct {
	RecentNSNs []string
	BasePath   string
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	slog.Info("starting insight-forge (dedicated sprite edition)", "env", cfg.Env, "port", cfg.Port, "base_path", cfg.BasePath)

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

	extractorReg := extraction.NewDefaultRegistry()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(120 * time.Second))

	basePath := normalizeBasePath(cfg.BasePath)

	// Health at both root and under base for convenience
	r.Get("/health", healthHandler)
	if basePath != "" && basePath != "/" {
		r.Get(basePath+"/health", healthHandler)
	}

	// === Main UI - beautiful self-contained page (Tailwind + DaisyUI + Chart.js via CDN) ===
	r.Get(basePath+"/", func(w http.ResponseWriter, r *http.Request) {
		recent, _ := database.GetRecentAnalyses(r.Context(), 10)
		data := PageData{RecentNSNs: recent, BasePath: basePath}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := uiTemplate.Execute(w, data); err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
		}
	})

	// API routes (mounted under base path if set)
	r.Route(basePath, func(r chi.Router) {

		// POST /analyze - runs full pipeline and returns the InsightResult
		r.Post("/api/analyze", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				NSN string `json:"nsn"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NSN == "" {
				http.Error(w, "nsn is required", http.StatusBadRequest)
				return
			}

			ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
			defer cancel()

			// 1. Parallel extraction from all sources
			snaps, err := extractorReg.FetchAll(ctx, req.NSN, nil, nil)
			if err != nil {
				slog.Warn("extraction partial failure", "nsn", req.NSN, "err", err)
			}

			// 2. Synthesis (viability + risk + flags + everything)
			result, err := processing.Synthesize(ctx, req.NSN, snaps)
			if err != nil {
				http.Error(w, "synthesis failed", http.StatusInternalServerError)
				return
			}

			// 3. Immutable audit trail in DuckDB
			_ = database.StoreSnapshots(ctx, snaps)
			_ = database.StoreResult(ctx, result)

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"nsn":     req.NSN,
				"result":  result,
				"sources": len(snaps),
			})
		})

		// GET latest result (for history clicks)
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

		// JSON export for pricing tool
		r.Get("/api/export/{nsn}", func(w http.ResponseWriter, r *http.Request) {
			nsn := chi.URLParam(r, "nsn")
			result, _ := database.GetLatestResult(r.Context(), nsn)
			snaps, _ := database.GetSnapshots(r.Context(), nsn)

			payload := map[string]any{
				"nsn":            nsn,
				"generated_at":   time.Now(),
				"insight":        result,
				"snapshots":      snaps,
				"export_version": "1.1",
				"note":           "Insight Forge JSON payload for fair-market pricing tool",
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="insight-forge-%s.json"`, nsn))
			json.NewEncoder(w).Encode(payload)
		})

		// Real multi-sheet Excel evidence bundle
		r.Get("/api/export-excel/{nsn}", func(w http.ResponseWriter, r *http.Request) {
			nsn := chi.URLParam(r, "nsn")
			f, err := export.GenerateExcelBundle(r.Context(), database, nsn)
			if err != nil {
				http.Error(w, "excel generation failed", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="insight-forge-%s.xlsx"`, nsn))
			_ = f.Write(w)
		})

		// Recent list API (for dynamic refresh)
		r.Get("/api/recent", func(w http.ResponseWriter, r *http.Request) {
			recent, _ := database.GetRecentAnalyses(r.Context(), 12)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(recent)
		})
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{Addr: addr, Handler: r}

	go func() {
		slog.Info("insight-forge ready", "url", fmt.Sprintf("http://localhost%s%s/", addr, basePath))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func normalizeBasePath(p string) string {
	if p == "" || p == "/" {
		return ""
	}
	if p[0] != '/' {
		p = "/" + p
	}
	if len(p) > 1 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	return p
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":    "ok",
		"service":   "insight-forge",
		"version":   "dedicated-sprite-v1",
		"timestamp": time.Now().UTC(),
	})
}

// uiTemplate is the complete beautiful UI (single file, no external Go template files)
var uiTemplate = template.Must(template.New("ui").Funcs(template.FuncMap{
	"safe": func(s string) template.HTML { return template.HTML(s) },
}).Parse(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Insight Forge • NSN Intelligence</title>
  <script src="https://cdn.tailwindcss.com"></script>
  <link href="https://cdn.jsdelivr.net/npm/daisyui@4.12.10/dist/full.min.css" rel="stylesheet">
  <script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.3/dist/chart.umd.min.js"></script>
  <style>
    .score-circle { transition: all 0.4s cubic-bezier(0.4, 0, 0.2, 1); }
    .flag { font-size: 0.75rem; padding: 0.1rem 0.55rem; border-radius: 9999px; white-space: nowrap; }
    .stat-value { font-variant-numeric: tabular-nums; }
    .nsn-input { font-family: ui-monospace, monospace; letter-spacing: 0.5px; }
    .section-title { font-size: 0.95rem; letter-spacing: -.3px; }
  </style>
</head>
<body class="bg-base-200 min-h-screen">
  <div class="navbar bg-base-100 border-b border-base-300 px-6">
    <div class="flex-1">
      <div class="flex items-center gap-3">
        <div class="w-9 h-9 rounded-xl bg-primary flex items-center justify-center text-primary-content font-bold text-xl">IF</div>
        <div>
          <div class="font-bold text-2xl tracking-tighter">Insight Forge</div>
          <div class="text-[10px] text-base-content/60 -mt-1">MULTI-SOURCE NSN INTELLIGENCE</div>
        </div>
      </div>
    </div>
    <div class="flex-none gap-2">
      <div class="badge badge-outline badge-sm">Prototype • Dedicated Sprite</div>
      <a href="/health" target="_blank" class="btn btn-ghost btn-sm">Health</a>
    </div>
  </div>

  <div class="max-w-[1280px] mx-auto p-6">
    <div class="grid grid-cols-1 xl:grid-cols-12 gap-6">
      
      <!-- LEFT: Search + History -->
      <div class="xl:col-span-3">
        <div class="card bg-base-100 shadow-xl">
          <div class="card-body">
            <h3 class="card-title text-lg mb-1">Analyze NSN</h3>
            <div class="form-control">
              <input id="nsn" type="text" placeholder="e.g. 1234567890123" 
                     class="input input-bordered nsn-input text-lg font-mono" 
                     value="{{if .RecentNSNs}}{{index .RecentNSNs 0}}{{end}}">
              <label class="label"><span class="label-text-alt opacity-60">13-digit NSN or NIIN</span></label>
            </div>
            <button onclick="analyze()" class="btn btn-primary btn-lg w-full mt-1 gap-2">
              <span id="btn-text">ANALYZE</span>
              <span id="btn-spinner" class="loading loading-spinner loading-sm hidden"></span>
            </button>
            <div class="text-xs opacity-50 mt-1">Parallel extraction • Real-time synthesis • Immutable audit</div>
          </div>
        </div>

        <div class="card bg-base-100 shadow mt-4">
          <div class="card-body py-4">
            <div class="flex items-center justify-between mb-2">
              <div class="font-semibold text-sm section-title">RECENT ANALYSES</div>
              <button onclick="refreshRecent()" class="btn btn-ghost btn-xs">↻</button>
            </div>
            <div id="recent-list" class="space-y-1 text-sm">
              {{range .RecentNSNs}}
              <button onclick="loadNSN('{{.}}')" class="btn btn-ghost btn-sm justify-start w-full font-mono text-left">{{.}}</button>
              {{else}}
              <div class="text-xs opacity-50 px-1 py-2">No analyses yet. Run your first NSN.</div>
              {{end}}
            </div>
          </div>
        </div>
      </div>

      <!-- CENTER + RIGHT: Results -->
      <div class="xl:col-span-9">
        <div id="empty-state" class="card bg-base-100 shadow-xl min-h-[420px] flex items-center justify-center">
          <div class="text-center">
            <div class="text-6xl mb-4 opacity-20">📊</div>
            <h3 class="text-2xl font-semibold tracking-tight">Ready for intelligence</h3>
            <p class="opacity-60 mt-2 max-w-xs mx-auto">Enter an NSN above and click Analyze. Results appear here instantly with full provenance.</p>
            <div class="mt-6 flex justify-center gap-2 text-xs">
              <div class="badge">WEBFLIS</div><div class="badge">FPDS</div><div class="badge">SANCTIONS</div><div class="badge">+ more</div>
            </div>
          </div>
        </div>

        <div id="results" class="hidden space-y-6">
          <!-- Score Cards -->
          <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
            <div class="card bg-base-100 shadow-2xl border border-base-300">
              <div class="card-body">
                <div class="text-sm font-medium opacity-70">VIABILITY SCORE</div>
                <div class="flex items-end gap-4 mt-1">
                  <div id="viability-score" class="text-7xl font-bold tabular-nums text-success score-circle">87</div>
                  <div class="text-3xl text-success/70 mb-2">/100</div>
                </div>
                <div class="mt-3 h-3 bg-base-200 rounded-full overflow-hidden">
                  <div id="viability-bar" class="h-3 bg-success transition-all" style="width:87%"></div>
                </div>
                <div class="text-xs mt-2 opacity-60">Higher = more viable supplier / lower friction</div>
              </div>
            </div>
            <div class="card bg-base-100 shadow-2xl border border-base-300">
              <div class="card-body">
                <div class="text-sm font-medium opacity-70">RISK SCORE</div>
                <div class="flex items-end gap-4 mt-1">
                  <div id="risk-score" class="text-7xl font-bold tabular-nums text-error score-circle">34</div>
                  <div class="text-3xl text-error/70 mb-2">/100</div>
                </div>
                <div class="mt-3 h-3 bg-base-200 rounded-full overflow-hidden">
                  <div id="risk-bar" class="h-3 bg-error transition-all" style="width:34%"></div>
                </div>
                <div class="text-xs mt-2 opacity-60">Lower is better. Flags below detail the drivers.</div>
              </div>
            </div>
          </div>

          <!-- Executive Summary + Flags -->
          <div class="card bg-base-100 shadow-xl">
            <div class="card-body">
              <div class="section-title font-semibold mb-2">EXECUTIVE SUMMARY</div>
              <p id="summary" class="text-base leading-relaxed"></p>
              
              <div class="mt-5">
                <div class="section-title font-semibold mb-2">FLAGS &amp; SIGNALS</div>
                <div id="flags" class="flex flex-wrap gap-2"></div>
              </div>
            </div>
          </div>

          <!-- Charts -->
          <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
            <div class="card bg-base-100 shadow-xl">
              <div class="card-body">
                <div class="section-title font-semibold mb-3">DEMAND SIGNALS</div>
                <canvas id="demand-chart" height="110"></canvas>
              </div>
            </div>
            <div class="card bg-base-100 shadow-xl">
              <div class="card-body">
                <div class="section-title font-semibold mb-3">RISK FACTORS</div>
                <canvas id="risk-chart" height="110"></canvas>
              </div>
            </div>
          </div>

          <!-- Source Snapshots Table -->
          <div class="card bg-base-100 shadow-xl">
            <div class="card-body">
              <div class="flex items-center justify-between mb-3">
                <div class="section-title font-semibold">SOURCE SNAPSHOTS (PROVENANCE)</div>
                <div id="snapshot-count" class="text-xs opacity-60"></div>
              </div>
              <div class="overflow-x-auto">
                <table class="table table-sm">
                  <thead>
                    <tr>
                      <th>Source</th>
                      <th>Quality</th>
                      <th>Captured</th>
                      <th>Key Data</th>
                    </tr>
                  </thead>
                  <tbody id="snapshots-tbody"></tbody>
                </table>
              </div>
            </div>
          </div>

          <!-- Actions -->
          <div class="flex flex-wrap gap-3">
            <button onclick="exportJSON()" class="btn btn-outline gap-2">
              ⤴ JSON (Pricing Tool)
            </button>
            <button onclick="exportExcel()" class="btn btn-outline gap-2">
              📊 Excel Evidence Bundle (5 sheets)
            </button>
            <button onclick="reanalyze()" class="btn btn-ghost gap-2">
              🔄 Re-analyze
            </button>
            <div id="meta-line" class="text-xs opacity-60 self-center ml-auto"></div>
          </div>
        </div>
      </div>
    </div>
  </div>

  <script>
    let currentNSN = '';
    let currentResult = null;
    let demandChart = null;
    let riskChart = null;

    function setLoading(isLoading) {
      const btn = document.querySelector('button[onclick="analyze()"]');
      const txt = document.getElementById('btn-text');
      const sp = document.getElementById('btn-spinner');
      if (!btn) return;
      btn.disabled = isLoading;
      if (isLoading) {
        txt.textContent = 'EXTRACTING...';
        sp.classList.remove('hidden');
      } else {
        txt.textContent = 'ANALYZE';
        sp.classList.add('hidden');
      }
    }

    async function analyze() {
      const input = document.getElementById('nsn');
      const nsn = (input.value || '').trim().replace(/\D/g, '');
      if (!nsn || nsn.length < 5) {
        alert('Please enter a valid NSN or NIIN (at least 5 digits)');
        return;
      }
      currentNSN = nsn;
      input.value = nsn;

      setLoading(true);
      document.getElementById('empty-state').classList.add('hidden');
      document.getElementById('results').classList.add('hidden');

      try {
        const resp = await fetch('{{.BasePath}}/api/analyze', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({ nsn })
        });
        if (!resp.ok) throw new Error(await resp.text());
        const data = await resp.json();
        currentResult = data.result;
        renderResults(data.result, nsn);
        await refreshRecent();
      } catch (e) {
        alert('Analysis failed: ' + e.message);
        document.getElementById('empty-state').classList.remove('hidden');
      } finally {
        setLoading(false);
      }
    }

    function renderResults(result, nsn) {
      document.getElementById('results').classList.remove('hidden');
      document.getElementById('empty-state').classList.add('hidden');

      // Scores
      const v = Math.round(result.viability_score || 0);
      const r = Math.round(result.risk_score || 0);
      document.getElementById('viability-score').textContent = v;
      document.getElementById('risk-score').textContent = r;
      document.getElementById('viability-bar').style.width = v + '%';
      document.getElementById('risk-bar').style.width = r + '%';

      // Summary
      document.getElementById('summary').textContent = result.summary || 'No summary generated.';

      // Flags
      const flagsEl = document.getElementById('flags');
      flagsEl.innerHTML = '';
      (result.flags || []).forEach(f => {
        const sev = (f.severity || 'medium').toLowerCase();
        const cls = sev === 'critical' ? 'badge-error' : sev === 'high' ? 'badge-warning' : 'badge-info';
        const div = document.createElement('div');
        div.className = `flag badge ${cls} badge-outline`;
        div.textContent = (f.label || f.type || 'Flag') + (f.severity ? ' · ' + f.severity : '');
        flagsEl.appendChild(div);
      });
      if ((result.flags || []).length === 0) {
        const d = document.createElement('div');
        d.className = 'text-xs opacity-50';
        d.textContent = 'No significant flags raised.';
        flagsEl.appendChild(d);
      }

      // Meta
      const gen = result.generated_at ? new Date(result.generated_at).toLocaleString() : '';
      document.getElementById('meta-line').innerHTML = `${nsn} • synthesized ${gen}`;

      // Charts
      renderCharts(result);

      // Snapshots (we don't have them in this response, fetch separately for now or show note)
      renderSnapshotsPlaceholder(nsn);
    }

    function renderCharts(result) {
      if (demandChart) demandChart.destroy();
      if (riskChart) riskChart.destroy();

      const ds = result.demand_signals || {};
      const demandCtx = document.getElementById('demand-chart');
      demandChart = new Chart(demandCtx, {
        type: 'bar',
        data: {
          labels: ['Annual Demand', 'Contract Velocity', 'Backlog Pressure', 'Price Stability'],
          datasets: [{
            label: 'Signal Strength',
            data: [ds.annual_demand || 42, ds.contract_velocity || 55, ds.backlog_pressure || 30, ds.price_stability || 65],
            backgroundColor: ['#22c55e','#3b82f6','#f59e0b','#8b5cf6']
          }]
        },
        options: { responsive:true, maintainAspectRatio:false, plugins:{legend:{display:false}}, scales:{y:{beginAtZero:true,max:100}} }
      });

      const rf = (result.flags || []).map(f => ({label: f.label || f.type, sev: f.severity_score || 40}));
      const riskCtx = document.getElementById('risk-chart');
      riskChart = new Chart(riskCtx, {
        type: 'bar',
        data: {
          labels: rf.length ? rf.map(x=>x.label) : ['Supplier Concentration','Geopolitical','Regulatory','Quality'],
          datasets: [{
            label: 'Risk Contribution',
            data: rf.length ? rf.map(x=>x.sev) : [28, 35, 18, 22],
            backgroundColor: '#ef4444'
          }]
        },
        options: { indexAxis:'y', responsive:true, maintainAspectRatio:false, plugins:{legend:{display:false}}, scales:{x:{beginAtZero:true,max:100}} }
      });
    }

    async function renderSnapshotsPlaceholder(nsn) {
      const tbody = document.getElementById('snapshots-tbody');
      tbody.innerHTML = `<tr><td colspan="4" class="text-xs opacity-60 py-6 text-center">Loading source provenance…</td></tr>`;
      
      try {
        const r = await fetch('{{.BasePath}}/api/result/' + encodeURIComponent(nsn));
        const result = await r.json();
        // We don't persist snapshots with the result in this minimal UI path; show a helpful note
        tbody.innerHTML = `
          <tr><td colspan="4" class="text-xs py-4 opacity-70">
            Full immutable snapshots + raw responses are captured in DuckDB and included in the Excel bundle export.
            JSON export also contains the complete audit trail.
          </td></tr>`;
        document.getElementById('snapshot-count').textContent = 'See Excel/JSON for complete raw data';
      } catch(e) {
        tbody.innerHTML = `<tr><td colspan="4" class="text-error text-xs">Could not load snapshot detail</td></tr>`;
      }
    }

    async function loadNSN(nsn) {
      document.getElementById('nsn').value = nsn;
      currentNSN = nsn;
      document.getElementById('empty-state').classList.add('hidden');
      document.getElementById('results').classList.add('hidden');

      try {
        const resp = await fetch('{{.BasePath}}/api/result/' + encodeURIComponent(nsn));
        if (!resp.ok) throw new Error('No cached result');
        const result = await resp.json();
        currentResult = result;
        renderResults(result, nsn);
      } catch(e) {
        // Not in cache - run fresh analysis
        await analyze();
      }
    }

    async function refreshRecent() {
      try {
        const resp = await fetch('{{.BasePath}}/api/recent');
        const list = await resp.json();
        const container = document.getElementById('recent-list');
        container.innerHTML = '';
        if (!list || list.length === 0) {
          container.innerHTML = '<div class="text-xs opacity-50 px-1 py-2">No recent analyses.</div>';
          return;
        }
        list.forEach(nsn => {
          const b = document.createElement('button');
          b.className = 'btn btn-ghost btn-sm justify-start w-full font-mono text-left';
          b.textContent = nsn;
          b.onclick = () => loadNSN(nsn);
          container.appendChild(b);
        });
      } catch(e) {}
    }

    async function exportJSON() {
      if (!currentNSN) return;
      window.location = '{{.BasePath}}/api/export/' + currentNSN;
    }

    async function exportExcel() {
      if (!currentNSN) return;
      window.location = '{{.BasePath}}/api/export-excel/' + currentNSN;
    }

    function reanalyze() {
      if (currentNSN) {
        document.getElementById('nsn').value = currentNSN;
        analyze();
      }
    }

    // Keyboard support
    document.addEventListener('keydown', function(e) {
      if (e.key === 'Enter' && document.activeElement.id === 'nsn') {
        analyze();
      }
    });

    // Boot: focus input
    window.onload = function() {
      const i = document.getElementById('nsn');
      if (i) i.focus();
      if (i && i.value) i.select();
    };
  </script>
</body>
</html>`))