package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strconv"

	"github.com/bmcelhaney/insight-forge/internal/config"
	"github.com/bmcelhaney/insight-forge/internal/extraction"
	"github.com/bmcelhaney/insight-forge/internal/processing"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
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

	extractorReg := extraction.NewDefaultRegistry()

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Serve the clean, self-contained professional analyst dashboard (no Tailwind CDN)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./static/index.html")
	})

	// Health (used by reset.sh / test_release.sh gates)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"service": "insight-forge",
			"version": "analyst-v2-gated",
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

	addr := ":" + strconv.Itoa(cfg.Port)
	fmt.Printf("Insight Forge Analyst Platform running on %s\n", addr)
	http.ListenAndServe(addr, r)
}
