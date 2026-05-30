package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/bmcelhaney/insight-forge/internal/config"
	"github.com/bmcelhaney/insight-forge/internal/extraction"
	"github.com/bmcelhaney/insight-forge/internal/processing"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// These are set at build time via -ldflags in reset.sh
var (
	commit    = "dev"
	buildTime = "unknown"
)

func main() {
	portFlag := flag.Int("port", 0, "Override port from config (used by test_release.sh gate)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	if *portFlag > 0 {
		cfg.Port = *portFlag
	}

	samAPIKey := strings.TrimSpace(os.Getenv("SAM_API_KEY"))
	extractorReg := extraction.NewDefaultRegistry(samAPIKey)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Serve the clean, self-contained professional analyst dashboard (no Tailwind CDN)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./static/index.html")
	})

	// Health (used by reset.sh / test_release.sh gates)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		samKeyPresent := os.Getenv("SAM_API_KEY") != ""
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":          "ok",
			"service":         "insight-forge",
			"commit":          commit,
			"buildTime":       buildTime,
			"version":         "analyst-v2-gated",
			"real_fpds_active": samKeyPresent,
			"note":            "If real_fpds_active=true, FPDS will use live SAM.gov data when available",
		})
	})

	// Version endpoint for deployment verification
	r.Get("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"commit":    commit,
			"buildTime": buildTime,
		})
	})

	// Main analysis endpoint - enhanced synthesis with rich AbilityOne-aware output
	r.Post("/api/analyze", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			NSN string `json:"nsn"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NSN == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "nsn (9 or 13 digits) is required"})
			return
		}

		snaps, _ := extractorReg.FetchAll(r.Context(), req.NSN, nil, nil)
		result, _ := processing.Synthesize(r.Context(), req.NSN, snaps)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"nsn":    req.NSN,
			"result": result,
		})
	})

	// JSON export for pricing tool
	r.Get("/api/export/json/{nsn}", func(w http.ResponseWriter, r *http.Request) {
		nsn := chi.URLParam(r, "nsn")
		snaps, _ := extractorReg.FetchAll(r.Context(), nsn, nil, nil)
		result, _ := processing.Synthesize(r.Context(), nsn, snaps)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="insight-forge-%s.json"`, nsn))
		json.NewEncoder(w).Encode(result)
	})

	// Basic Excel export (placeholder for now - full version in next iteration)
	r.Get("/api/export-excel/{nsn}", func(w http.ResponseWriter, r *http.Request) {
		nsn := chi.URLParam(r, "nsn")
		snaps, _ := extractorReg.FetchAll(r.Context(), nsn, nil, nil)
		result, _ := processing.Synthesize(r.Context(), nsn, snaps)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"note":   "Full multi-sheet Excel coming in next update",
			"nsn":    nsn,
			"result": result,
			"snaps":  len(snaps),
		})
	})

	// Debug endpoint to test real FPDS / SAM.gov integration
	r.Get("/debug/fpds/{nsn}", func(w http.ResponseWriter, r *http.Request) {
		nsn := chi.URLParam(r, "nsn")
		// Only fetch FPDS so it's clean
		snaps, _ := extractorReg.FetchAll(r.Context(), nsn, []string{"FPDS"}, nil)

		isLive := false
		var samError string
		if len(snaps) > 0 {
			if ds, ok := snaps[0].RawResponse["data_source"].(string); ok && ds == "live_sam_gov" {
				isLive = true
			}
			if errStr, ok := snaps[0].RawResponse["sam_call_error"].(string); ok {
				samError = errStr
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"nsn":             nsn,
			"using_real_sam":  isLive,
			"sam_call_error":  samError,
			"fpds_snapshots":  snaps,
			"sam_key_present": os.Getenv("SAM_API_KEY") != "",
			"note":            "If using_real_sam is true → live SAM.gov data. If sam_call_error is present → real call was attempted but failed.",
		})
	})

	addr := ":" + strconv.Itoa(cfg.Port)
	fmt.Printf("\n")
	fmt.Printf("========================================\n")
	fmt.Printf("  Insight Forge Analyst Platform\n")
	fmt.Printf("  Commit:    %s\n", commit)
	fmt.Printf("  Built:     %s\n", buildTime)
	fmt.Printf("  Listening: %s\n", addr)
	fmt.Printf("========================================\n\n")
	http.ListenAndServe(addr, r)
}
