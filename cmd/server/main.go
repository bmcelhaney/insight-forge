package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"time"

	"github.com/bmcelhaney/insight-forge/internal/clickhouse"
	"github.com/bmcelhaney/insight-forge/internal/config"
	"github.com/bmcelhaney/insight-forge/internal/extraction"
	"github.com/bmcelhaney/insight-forge/internal/models"
	"github.com/bmcelhaney/insight-forge/internal/processing"
	"github.com/bmcelhaney/insight-forge/internal/screenshot"
	"github.com/bmcelhaney/insight-forge/internal/storage"
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

	// SerpAPI Google Shopping for commercial market prices / better product links.
	// Immersive Product (P2) is optional extra quota — disable with IF_SERPAPI_IMMERSIVE=false.
	if cfg.SerpAPIEnabled && cfg.SerpAPIConfigured {
		processing.ConfigureSerpAPI(cfg.SerpAPIKey, cfg.SerpAPINum, cfg.SerpAPIImmersive)
		imm := "shopping only"
		if cfg.SerpAPIImmersive {
			imm = "shopping + immersive product"
		}
		fmt.Printf("SerpAPI: enabled (%s, num=%d)\n", imm, cfg.SerpAPINum)
	} else if cfg.SerpAPIEnabled {
		fmt.Printf("SerpAPI: enabled but IF_SERPAPI_KEY missing — place key in .env.serpapi (gitignored)\n")
	} else {
		fmt.Printf("SerpAPI: disabled\n")
	}

	// UPCItemDB paid DEV/PRO key → /prod/v1 with user_key header (never log the key).
	if cfg.UPCItemDBEnabled && cfg.UPCItemDBConfigured {
		processing.ConfigureUPCItemDB(cfg.UPCItemDBKey)
		fmt.Printf("UPCItemDB: paid plan enabled (/prod/v1)\n")
	} else if cfg.UPCItemDBEnabled {
		fmt.Printf("UPCItemDB: trial mode (/prod/trial) — place IF_UPCITEMDB_KEY in .env.upcitemdb (gitignored) for paid access\n")
	} else {
		fmt.Printf("UPCItemDB: disabled\n")
	}

	// Flags for UI integration-health banners (missing keys, runtime failures).
	processing.ConfigureIntegrationFlags(
		cfg.SerpAPIEnabled, cfg.SerpAPIConfigured,
		cfg.UPCItemDBEnabled, cfg.UPCItemDBConfigured,
	)

	// Tigris + page screenshots: PARKED (not used in production path).
	// Focus is pricing hits + reliable product URLs. To revive later:
	//   IF_TIGRIS_ENABLED=true + IF_SCREENSHOT_ENABLED=true + credentials in .env.tigris
	var tigrisStore *storage.Client
	var shotCapturer *screenshot.Capturer
	var shotWorker *screenshot.Worker
	if cfg.ScreenshotEnabled && cfg.TigrisEnabled {
		st, err := storage.NewTigrisClient(storage.TigrisConfig{
			Enabled:   true,
			Bucket:    cfg.TigrisBucket,
			Region:    cfg.TigrisRegion,
			Endpoint:  cfg.TigrisEndpoint,
			AccessKey: cfg.TigrisAccessKey,
			SecretKey: cfg.TigrisSecretKey,
		})
		if err != nil {
			fmt.Printf("Tigris: config error: %v\n", err)
		} else if st != nil {
			tigrisStore = st
			fmt.Printf("Tigris: enabled (bucket=%s endpoint=%s)\n", cfg.TigrisBucket, cfg.TigrisEndpoint)
			pingCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			if err := st.Ping(pingCtx); err != nil {
				fmt.Printf("Tigris: ping failed: %v\n", err)
			} else {
				fmt.Printf("Tigris: bucket reachable\n")
			}
			cancel()
		}
		shotTO := time.Duration(cfg.ScreenshotTimeoutMS) * time.Millisecond
		if shotTO <= 0 {
			shotTO = 10 * time.Second
		}
		backend := strings.TrimSpace(os.Getenv("IF_SCREENSHOT_BACKEND"))
		if backend == "" {
			backend = screenshot.BackendThum
		}
		shotCapturer = screenshot.NewCapturer(screenshot.Options{
			Backend:  backend,
			Timeout:  shotTO,
			Width:    1280,
			Height:   720,
			ThumAuth: strings.TrimSpace(os.Getenv("IF_THUM_AUTH")),
		})
		if shotCapturer.Available() && tigrisStore != nil {
			batchTO := 45 * time.Second
			if v := strings.TrimSpace(os.Getenv("IF_SCREENSHOT_BATCH_TIMEOUT_MS")); v != "" {
				if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
					batchTO = time.Duration(ms) * time.Millisecond
				}
			}
			shotWorker = screenshot.NewWorker(tigrisStore, shotCapturer, screenshot.ProofOptions{
				MaxPerRun:    cfg.ScreenshotMaxPerRun,
				Timeout:      shotTO,
				BatchTimeout: batchTO,
				PresignTTL:   time.Hour,
			})
			fmt.Printf("Screenshots: ENABLED (backend=%s max=%d/run)\n", shotCapturer.Backend(), cfg.ScreenshotMaxPerRun)
		}
	} else {
		fmt.Printf("Screenshots/Tigris: disabled (pricing hits + product URLs only; set IF_SCREENSHOT_ENABLED=true to re-enable)\n")
	}

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

	var chClient *clickhouse.Client
	if cfg.ClickHouseEnabled {
		c, err := clickhouse.New(clickhouse.Config{
			Host:     cfg.ClickHouseHost,
			Port:     cfg.ClickHousePort,
			Database: cfg.ClickHouseDatabase,
			User:     cfg.ClickHouseUser,
			Password: cfg.ClickHousePassword,
		})
		if err != nil {
			fmt.Printf("ClickHouse: config error: %v\n", err)
		} else {
			chClient = c
			fmt.Printf("ClickHouse: enabled (db=%s host=%s)\n", cfg.ClickHouseDatabase, cfg.ClickHouseHost)
			go func() {
				pingCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				if err := chClient.Ping(pingCtx); err != nil {
					fmt.Printf("ClickHouse: ping failed: %v\n", err)
				} else {
					fmt.Printf("ClickHouse: reachable\n")
				}
			}()
		}
	} else {
		fmt.Printf("ClickHouse: disabled (set CH_HOST/CH_USER/CH_PASSWORD to ingest analyze runs)\n")
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
		payload := map[string]any{
			"status":                "ok",
			"service":               "insight-forge",
			"commit":                commit,
			"buildTime":             buildTime,
			"version":               "analyst-v2-gated",
			"note":                  "Award data prefers USAspending.gov (real, public). SAM.gov path is currently disabled.",
			"partsbase_enabled":     cfg.PartsBaseEnabled,
			"partsbase_configured":  cfg.PartsBaseConfigured,
			"partsbase_registered":  extractorReg.PartsBaseRegistered(),
			"partsbase_env_files":   cfg.PartsBaseEnvFilesLoaded,
			"serpapi_enabled":       cfg.SerpAPIEnabled,
			"serpapi_configured":    cfg.SerpAPIConfigured && processing.SerpAPIEnabled(),
			"serpapi_num":           cfg.SerpAPINum,
			"serpapi_immersive":     cfg.SerpAPIImmersive && processing.SerpAPIImmersiveEnabled(),
			"upcitemdb_enabled":     cfg.UPCItemDBEnabled,
			"upcitemdb_configured":  cfg.UPCItemDBConfigured && processing.UPCItemDBConfigured(),
			"upcitemdb_plan":        map[bool]string{true: "v1", false: "trial"}[processing.UPCItemDBConfigured()],
			"tigris_configured":     tigrisStore != nil,
			"tigris_bucket":         cfg.TigrisBucket,
			"screenshots_enabled":   shotWorker != nil && shotWorker.Available(),
			"screenshots_async":     true,
			"clickhouse_configured": chClient != nil,
			"clickhouse_database":   cfg.ClickHouseDatabase,
			"screenshots_max":       cfg.ScreenshotMaxPerRun,
			"screenshots_backend": func() string {
				if shotCapturer != nil {
					return shotCapturer.Backend()
				}
				return ""
			}(),
		}
		if chClient != nil {
			ok, errText, at := chClient.LastStatus()
			payload["clickhouse_ok"] = ok
			if errText != "" {
				payload["clickhouse_error"] = errText
			}
			if !at.IsZero() {
				payload["clickhouse_checked_at"] = at.Format(time.RFC3339)
			}
		}
		// Last observed PartsBase fetch outcome (for UI source-status banner).
		var pbStatus *models.PartsBaseStatus
		if st, ok := extractorReg.PartsBaseLastStatus(); ok {
			payload["partsbase_live"] = st.Live
			payload["partsbase_data_source"] = st.DataSource
			payload["partsbase_message"] = st.Message
			if st.Error != "" {
				payload["partsbase_error"] = st.Error
			}
			if !st.CheckedAt.IsZero() {
				payload["partsbase_checked_at"] = st.CheckedAt.Format("2006-01-02T15:04:05Z")
			}
			// Only report ok=true/false after a real query (not the initial "not_checked" state).
			if st.DataSource == "" || st.DataSource == "not_checked" {
				payload["partsbase_ok"] = nil
				payload["partsbase_message"] = "PartsBase registered; awaiting first analysis query."
				pbStatus = &models.PartsBaseStatus{
					OK: true, Enabled: cfg.PartsBaseEnabled, Configured: cfg.PartsBaseConfigured,
					DataSource: "not_checked", Message: "PartsBase registered; awaiting first analysis query.",
				}
			} else {
				payload["partsbase_ok"] = st.OK
				pbStatus = &models.PartsBaseStatus{
					OK: st.OK, Enabled: cfg.PartsBaseEnabled, Configured: cfg.PartsBaseConfigured,
					Live: st.Live, DataSource: st.DataSource, Error: st.Error, Message: st.Message,
				}
			}
		} else if cfg.PartsBaseEnabled && !cfg.PartsBaseConfigured {
			payload["partsbase_ok"] = false
			payload["partsbase_message"] = "PartsBase is enabled but OAuth credentials are not loaded."
			pbStatus = &models.PartsBaseStatus{
				OK: false, Enabled: true, Configured: false,
				Message: "PartsBase is enabled but OAuth credentials are not loaded.",
			}
		} else if !cfg.PartsBaseEnabled {
			payload["partsbase_ok"] = false
			payload["partsbase_message"] = "PartsBase integration is disabled."
			pbStatus = &models.PartsBaseStatus{
				OK: false, Enabled: false, Configured: cfg.PartsBaseConfigured,
				Message: "PartsBase integration is disabled.",
			}
		} else {
			payload["partsbase_ok"] = nil // not yet queried this process
			payload["partsbase_message"] = "PartsBase registered; awaiting first analysis query."
			pbStatus = &models.PartsBaseStatus{
				OK: true, Enabled: true, Configured: cfg.PartsBaseConfigured,
				DataSource: "not_checked", Message: "PartsBase registered; awaiting first analysis query.",
			}
		}
		// Multi-API health for UI banners (PartsBase + SerpAPI + UPCItemDB).
		payload["integration_health"] = processing.IntegrationHealthSnapshot(
			cfg.SerpAPIEnabled, cfg.SerpAPIConfigured,
			cfg.UPCItemDBEnabled, cfg.UPCItemDBConfigured,
			pbStatus,
		)
		json.NewEncoder(w).Encode(payload)
	})

	// Version endpoint for deployment verification
	r.Get("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"commit":    commit,
			"buildTime": buildTime,
		})
	})

	// runAnalyze synthesizes an NSN and builds the data-capture document.
	// One builder for UI export, POST /api/analyze, and GET /api/export/data.
	// serpImmersive nil → server default (IF_SERPAPI_IMMERSIVE, default true).
	// captureScreenshots is ignored unless screenshots are explicitly re-enabled
	// (IF_SCREENSHOT_ENABLED + Tigris). Default path: pricing hits + reliable URLs only.
	runAnalyze := func(ctx context.Context, nsn string, serpImmersive *bool, captureScreenshots bool) (models.InsightResult, models.DataCaptureDocument) {
		if serpImmersive != nil {
			ctx = processing.WithSerpImmersive(ctx, *serpImmersive)
		} else {
			ctx = processing.WithSerpImmersive(ctx, processing.SerpAPIImmersiveDefault())
		}
		t0 := time.Now()
		snaps, _ := extractorReg.FetchAll(ctx, nsn, nil, nil)
		extractMS := time.Since(t0).Milliseconds()
		t1 := time.Now()
		result, _ := processing.Synthesize(ctx, nsn, snaps)
		synthMS := time.Since(t1).Milliseconds()
		t2 := time.Now()
		doc := processing.BuildDataCaptureDocument(result, snaps, processing.DataCaptureMeta{
			Commit:    commit,
			BuildTime: buildTime,
		})
		dcMS := time.Since(t2).Milliseconds()
		doc.Timings = processing.AssembleAnalyzeTimings(extractMS, synthMS, dcMS, time.Since(t0).Milliseconds(), snaps, result.PhaseTimings)
		wantShots := cfg.ScreenshotEnabled && (captureScreenshots || cfg.ScreenshotOnAnalyze)
		if wantShots && shotWorker != nil && shotWorker.Available() {
			_ = shotWorker.MarkPendingAndEnqueue(&doc)
		}
		if chClient != nil {
			ingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			ing := chClient.IngestAnalysis(ingCtx, doc)
			cancel()
			doc.ClickHouse = ing.ToModel()
			if ing.Error != "" {
				fmt.Printf("ClickHouse ingest failed analysis_id=%s: %s\n", doc.AnalysisID, ing.Error)
			} else if ing.Written {
				fmt.Printf("ClickHouse ingest ok analysis_id=%s analyses=%d hits=%d priced=%d\n",
					doc.AnalysisID, ing.Analyses, ing.Hits, ing.PricedHits)
			}
		} else {
			doc.ClickHouse = &models.DataCaptureClickHouse{Enabled: false}
		}
		return result, doc
	}

	jobs := newBatchStore()

	// parseSerpImmersive reads optional body field or query param.
	// Omitted → nil (use server default = immersive on).
	parseSerpImmersive := func(body *bool, query string) *bool {
		if body != nil {
			return body
		}
		q := strings.TrimSpace(strings.ToLower(query))
		if q == "" {
			return nil
		}
		switch q {
		case "1", "true", "yes", "on", "immersive":
			v := true
			return &v
		case "0", "false", "no", "off", "shopping", "normal":
			v := false
			return &v
		}
		return nil
	}

	// writeDataCaptureJSON writes the data-capture document with the same
	// encoding used by the UI "Export JSON (Data Capture)" download path.
	writeDataCaptureJSON := func(w http.ResponseWriter, doc models.DataCaptureDocument, nsn string, asAttachment bool) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if asAttachment {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="insight-forge-data-%s.json"`, nsn))
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(doc)
	}

	// Multi-API quota / burn status (SerpAPI Account API is free; UPC has no public remaining counter).
	r.Get("/api/quotas", func(w http.ResponseWriter, r *http.Request) {
		force := r.URL.Query().Get("refresh") == "1" || r.URL.Query().Get("force") == "1"
		payload := processing.BuildAPIQuotas(r.Context(), force)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(payload)
	})

	// Async screenshot proof status for an analysis run (poll after capture_screenshots).
	r.Get("/api/proofs/{analysisID}", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(chi.URLParam(r, "analysisID"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if id == "" || shotWorker == nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "proofs unavailable"})
			return
		}
		run := shotWorker.GetRun(id)
		if run == nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "analysis_id not found or expired"})
			return
		}
		_ = json.NewEncoder(w).Encode(run)
	})

	// UI image proxy — serves Tigris objects without embedding short-lived URLs in JSON.
	// Path is stable for the session: /api/proofs/{analysis_id}/hits/{hit_id}/image
	r.Get("/api/proofs/{analysisID}/hits/{hitID}/image", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(chi.URLParam(r, "analysisID"))
		hitID := strings.TrimSpace(chi.URLParam(r, "hitID"))
		if id == "" || hitID == "" || shotWorker == nil || tigrisStore == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		run := shotWorker.GetRun(id)
		if run == nil {
			http.Error(w, "analysis not found", http.StatusNotFound)
			return
		}
		shot := run.Hits[hitID]
		if shot == nil || shot.Status != "ready" || strings.TrimSpace(shot.ObjectKey) == "" {
			http.Error(w, "image not ready", http.StatusNotFound)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		data, ct, err := tigrisStore.GetObject(ctx, shot.ObjectKey)
		if err != nil {
			http.Error(w, "image fetch failed", http.StatusBadGateway)
			return
		}
		if ct == "" {
			ct = "image/png"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "private, max-age=300")
		_, _ = w.Write(data)
	})

	// Primary machine API: data-capture document only — identical body to
	// GET /api/export/data/{nsn} and to the UI Data Capture export (same builder).
	// Optional: "serp_immersive": true|false (default true / server IF_SERPAPI_IMMERSIVE).
	//
	// Screenshots are OFF unless the feature is re-enabled server-side and the
	// client explicitly requests them. Parked: focus on pricing + product URLs.
	parseCaptureScreenshots := func(body *bool, query string) bool {
		if !cfg.ScreenshotEnabled || shotWorker == nil || !shotWorker.Available() {
			return false
		}
		if body != nil {
			return *body
		}
		q := strings.TrimSpace(strings.ToLower(query))
		return q == "1" || q == "true" || q == "yes" || q == "on"
	}

	r.Post("/api/analyze", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			NSN                string `json:"nsn"`
			SerpImmersive      *bool  `json:"serp_immersive"`
			CaptureScreenshots *bool  `json:"capture_screenshots"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NSN == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "nsn (9 or 13 digits) is required"})
			return
		}

		shots := parseCaptureScreenshots(req.CaptureScreenshots, r.URL.Query().Get("capture_screenshots"))
		_, doc := runAnalyze(r.Context(), req.NSN, parseSerpImmersive(req.SerpImmersive, r.URL.Query().Get("serp_immersive")), shots)
		writeDataCaptureJSON(w, doc, req.NSN, false)
	})

	// Batch: latest unique NSNs from EBS.XXSC_XXSC_PLIMS_PRODUCTS, then sequential analyze.
	r.Post("/api/batch/analyze", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if chClient == nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "ClickHouse is not configured; batch requires EBS.XXSC_XXSC_PLIMS_PRODUCTS"})
			return
		}
		var req struct {
			Limit         int   `json:"limit"`
			SerpImmersive *bool `json:"serp_immersive"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		limit := req.Limit
		if limit <= 0 {
			limit = 5
		}
		if limit > clickhouse.MaxPlimsBatch {
			limit = clickhouse.MaxPlimsBatch
		}
		qctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		pick, err := chClient.LatestPlimsNSNs(qctx, limit)
		cancel()
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": "could not load PLIMS NSNs: " + err.Error()})
			return
		}
		imm := parseSerpImmersive(req.SerpImmersive, r.URL.Query().Get("serp_immersive"))
		job, err := jobs.start(pick, runAnalyze, imm)
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(job.snapshot())
	})

	r.Get("/api/batch/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		job := jobs.get(chi.URLParam(r, "id"))
		if job == nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "batch job not found"})
			return
		}
		json.NewEncoder(w).Encode(job.snapshot())
	})

	// Full insight payload for the Insight Forge UI and pricing-tool consumers.
	// data_capture is the same document type as POST /api/analyze (same builder).
	r.Post("/api/insight", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			NSN                string `json:"nsn"`
			SerpImmersive      *bool  `json:"serp_immersive"`
			CaptureScreenshots *bool  `json:"capture_screenshots"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NSN == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "nsn (9 or 13 digits) is required"})
			return
		}

		imm := parseSerpImmersive(req.SerpImmersive, r.URL.Query().Get("serp_immersive"))
		shots := parseCaptureScreenshots(req.CaptureScreenshots, r.URL.Query().Get("capture_screenshots"))
		result, doc := runAnalyze(r.Context(), req.NSN, imm, shots)
		usedImmersive := processing.SerpAPIImmersiveDefault()
		if imm != nil {
			usedImmersive = *imm
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		// data_capture is embedded for the UI export button — same struct as /api/analyze.
		_ = enc.Encode(map[string]any{
			"nsn":                req.NSN,
			"result":             result,
			"data_capture":       doc,
			"serp_immersive":     usedImmersive && processing.SerpAPIEnabled(),
			"analysis_id":        doc.AnalysisID,
			"clickhouse":         doc.ClickHouse,
			"screenshots_queued": shots && shotWorker != nil,
			"screenshots_async":  shotWorker != nil,
			"proofs_poll_url":    "",
		})
	})

	// JSON export for pricing tool (full InsightResult narrative + scores)
	r.Get("/api/export/json/{nsn}", func(w http.ResponseWriter, r *http.Request) {
		nsn := chi.URLParam(r, "nsn")
		imm := parseSerpImmersive(nil, r.URL.Query().Get("serp_immersive"))
		shots := parseCaptureScreenshots(nil, r.URL.Query().Get("capture_screenshots"))
		result, _ := runAnalyze(r.Context(), nsn, imm, shots)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="insight-forge-pricing-%s.json"`, nsn))
		json.NewEncoder(w).Encode(result)
	})

	// Data-capture file download — same JSON body as POST /api/analyze.
	r.Get("/api/export/data/{nsn}", func(w http.ResponseWriter, r *http.Request) {
		nsn := chi.URLParam(r, "nsn")
		imm := parseSerpImmersive(nil, r.URL.Query().Get("serp_immersive"))
		shots := parseCaptureScreenshots(nil, r.URL.Query().Get("capture_screenshots"))
		_, doc := runAnalyze(r.Context(), nsn, imm, shots)
		writeDataCaptureJSON(w, doc, nsn, true)
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

		out := map[string]any{
			"nsn":                  nsn,
			"data_source":          dataSource,
			"partsbase_snapshots":  snaps,
			"partsbase_enabled":    cfg.PartsBaseEnabled,
			"partsbase_configured": cfg.PartsBaseConfigured,
			"partsbase_registered": extractorReg.PartsBaseRegistered(),
		}
		if st, ok := extractorReg.PartsBaseLastStatus(); ok {
			out["partsbase_status"] = st
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
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
			"nsn":                  nsn,
			"ets_dataset":          etsDataset,
			"ets_matched_rows":     etsMatched,
			"ets_truncated":        etsTruncated || len(result.CommercialReferences) >= 200,
			"abilityone_com_price": abilityOnePrice,
			"abilityone_com_sku":   abilityOnePriceSKU,
			"commercial_refs":      len(result.CommercialReferences),
			"priced_count":         priced,
			"sources":              sources,
			"sample":               sample,
			"pricing_note":         "Primary live price source is AbilityOne.com catalog (dashed NSN). GSA Advantage HTML scrape is degraded after their SPA rewrite.",
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
