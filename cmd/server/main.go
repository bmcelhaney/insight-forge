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
	partsBaseCfg := extraction.PartsBaseConfig{
		Enabled:          cfg.PartsBaseEnabled,
		ClientID:         cfg.PartsBaseClientID,
		ClientSecret:     cfg.PartsBaseClientSecret,
		Username:         cfg.PartsBaseUsername,
		Password:         cfg.PartsBasePassword,
		AuthURL:          cfg.PartsBaseAuthURL,
		BaseURL:          cfg.PartsBaseBaseURL,
		GovDataPath:      cfg.PartsBaseGovDataPath,
		GovDataType:      cfg.PartsBaseGovDataType,
		GovDataStartDate: cfg.PartsBaseGovDataStart,
		GovDataSections:  cfg.PartsBaseGovDataSections,
		OAuthGrantType:   cfg.PartsBaseOAuthGrantType,
		OAuthScope:       cfg.PartsBaseOAuthScope,
		TimeoutSeconds:   cfg.PartsBaseTimeoutSeconds,
	}
	extractorReg := extraction.NewDefaultRegistry(samAPIKey, partsBaseCfg)

	// Surface PartsBase credential status without leaking secrets.
	if cfg.PartsBaseEnabled && cfg.PartsBaseConfigured {
		fmt.Printf("PartsBase: enabled (credentials loaded")
		if len(cfg.PartsBaseEnvFilesLoaded) > 0 {
			fmt.Printf(" from %s", strings.Join(cfg.PartsBaseEnvFilesLoaded, ", "))
		}
		fmt.Printf(")\n")
	} else if cfg.PartsBaseEnabled {
		fmt.Printf("PartsBase: enabled but credentials missing — extractor not registered. Place IF_PARTSBASE_* in .env.partsbase or the process environment.\n")
	} else {
		fmt.Printf("PartsBase: disabled\n")
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Serve the clean, self-contained professional analyst dashboard (no Tailwind CDN)
	// We inject the build commit directly into the HTML so the build number is always visible
	// even if JS fails or browser cache is involved.
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		htmlBytes, err := os.ReadFile("./static/index.html")
		if err != nil {
			http.Error(w, "Failed to load UI", http.StatusInternalServerError)
			return
		}
		htmlStr := string(htmlBytes)
		htmlStr = strings.ReplaceAll(htmlStr, "{{COMMIT}}", commit)
		htmlStr = strings.ReplaceAll(htmlStr, "{{BUILDTIME}}", buildTime)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(htmlStr))
	})

	// Health (used by reset.sh / test_release.sh gates)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":                 "ok",
			"service":                "insight-forge",
			"commit":                 commit,
			"buildTime":              buildTime,
			"version":                "analyst-v2-gated",
			"note":                   "Award data prefers USAspending.gov (real, public). SAM.gov path is currently disabled.",
			"partsbase_enabled":      cfg.PartsBaseEnabled,
			"partsbase_configured":   cfg.PartsBaseConfigured,
			"partsbase_env_files":    cfg.PartsBaseEnvFilesLoaded,
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

	// Debug endpoint for real award data (FPDS path)
	r.Get("/debug/fpds/{nsn}", func(w http.ResponseWriter, r *http.Request) {
		nsn := chi.URLParam(r, "nsn")
		snaps, _ := extractorReg.FetchAll(r.Context(), nsn, []string{"FPDS"}, nil)

		dataSource := "unknown"
		if len(snaps) > 0 {
			if ds, ok := snaps[0].RawResponse["data_source"].(string); ok {
				dataSource = ds
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"nsn":            nsn,
			"data_source":    dataSource,
			"fpds_snapshots": snaps,
			"note":           "data_source will be 'live_usaspending' (real) or 'prototype'. SAM.gov path is currently disabled.",
		})
	})

	// Debug endpoint for PartsBase GovData extraction
	r.Get("/debug/partsbase/{nsn}", func(w http.ResponseWriter, r *http.Request) {
		nsn := chi.URLParam(r, "nsn")
		snaps, _ := extractorReg.FetchAll(r.Context(), nsn, []string{"PARTSBASE"}, nil)

		dataSource := "unavailable"
		if len(snaps) > 0 {
			if ds, ok := snaps[0].RawResponse["data_source"].(string); ok && ds != "" {
				dataSource = ds
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"nsn":                 nsn,
			"data_source":         dataSource,
			"partsbase_snapshots": snaps,
			"partsbase_enabled":   cfg.PartsBaseEnabled,
		})
	})

	// Debug commercial / ETS coverage and pricing enrichment
	r.Get("/debug/commercial/{nsn}", func(w http.ResponseWriter, r *http.Request) {
		nsn := chi.URLParam(r, "nsn")
		snaps, _ := extractorReg.FetchAll(r.Context(), nsn, nil, nil)
		result, _ := processing.Synthesize(r.Context(), nsn, snaps)

		etsMatched := 0
		etsTruncated := false
		etsDataset := ""
		abilityOnePrice := ""
		abilityOnePriceSKU := ""
		for _, s := range snaps {
			if s.SourceCode == "ABILITYONE_ETS" {
				if n, ok := s.RawResponse["matched_rows_count"].(int); ok {
					etsMatched = n
				} else if f, ok := s.RawResponse["matched_rows_count"].(float64); ok {
					etsMatched = int(f)
				}
				if t, ok := s.RawResponse["references_truncated"].(bool); ok {
					etsTruncated = t
				}
				if name, ok := s.RawResponse["dataset_name"].(string); ok {
					etsDataset = name
				}
			}
			if s.SourceCode == "ABILITYONE_COMMERCE" {
				if p, ok := s.RawResponse["best_price"].(float64); ok && p > 0 {
					abilityOnePrice = fmt.Sprintf("$%.2f", p)
				} else if p, ok := s.RawResponse["best_price"].(string); ok && p != "" {
					abilityOnePrice = p
				}
				if sku, ok := s.RawResponse["best_sku"].(string); ok {
					abilityOnePriceSKU = sku
				}
			}
		}

		sources := map[string]int{}
		priced := 0
		for _, c := range result.CommercialReferences {
			src := c.Source
			if src == "" {
				src = "UNKNOWN"
			}
			sources[src]++
			if strings.TrimSpace(c.Price) != "" {
				priced++
			}
		}

		sampleN := 5
		if len(result.CommercialReferences) < sampleN {
			sampleN = len(result.CommercialReferences)
		}
		sample := result.CommercialReferences[:sampleN]

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"nsn":                     nsn,
			"ets_dataset":             etsDataset,
			"ets_matched_rows":        etsMatched,
			"ets_truncated":           etsTruncated || len(result.CommercialReferences) >= 200,
			"abilityone_com_price":    abilityOnePrice,
			"abilityone_com_sku":      abilityOnePriceSKU,
			"commercial_refs":         len(result.CommercialReferences),
			"priced_count":            priced,
			"sources":                 sources,
			"sample":                  sample,
			"pricing_note":            "Primary live price source is AbilityOne.com catalog (dashed NSN). GSA Advantage HTML scrape is degraded after their SPA rewrite.",
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
