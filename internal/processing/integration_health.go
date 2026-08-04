package processing

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// Runtime last-call status for commercial APIs (process-wide, for /health + UI).
type apiRuntimeStatus struct {
	OK        bool
	Checked   bool
	HTTPCode  int
	Error     string
	Message   string
	CheckedAt time.Time
}

var (
	serpStatusMu sync.RWMutex
	serpStatus   apiRuntimeStatus

	upcStatusMu sync.RWMutex
	upcStatus   apiRuntimeStatus

	// Feature flags from config (set at process start; never log secrets).
	integFlagsMu sync.RWMutex
	integFlags   struct {
		SerpEnabled, SerpConfigured bool
		UPCEnabled, UPCConfigured   bool
	}
)

// ConfigureIntegrationFlags stores enabled/configured state for health banners.
func ConfigureIntegrationFlags(serpEnabled, serpConfigured, upcEnabled, upcConfigured bool) {
	integFlagsMu.Lock()
	defer integFlagsMu.Unlock()
	integFlags.SerpEnabled = serpEnabled
	integFlags.SerpConfigured = serpConfigured
	integFlags.UPCEnabled = upcEnabled
	integFlags.UPCConfigured = upcConfigured
}

func integrationFlagsSnapshot() (serpEn, serpCfg, upcEn, upcCfg bool) {
	integFlagsMu.RLock()
	defer integFlagsMu.RUnlock()
	return integFlags.SerpEnabled, integFlags.SerpConfigured, integFlags.UPCEnabled, integFlags.UPCConfigured
}

func recordSerpAPIStatus(ok bool, httpCode int, errMsg, message string) {
	serpStatusMu.Lock()
	defer serpStatusMu.Unlock()
	serpStatus = apiRuntimeStatus{
		OK:        ok,
		Checked:   true,
		HTTPCode:  httpCode,
		Error:     sanitizeAPIError(errMsg),
		Message:   message,
		CheckedAt: time.Now().UTC(),
	}
}

func recordUPCItemDBStatus(ok bool, httpCode int, errMsg, message string) {
	upcStatusMu.Lock()
	defer upcStatusMu.Unlock()
	upcStatus = apiRuntimeStatus{
		OK:        ok,
		Checked:   true,
		HTTPCode:  httpCode,
		Error:     sanitizeAPIError(errMsg),
		Message:   message,
		CheckedAt: time.Now().UTC(),
	}
}

func getSerpAPIStatus() apiRuntimeStatus {
	serpStatusMu.RLock()
	defer serpStatusMu.RUnlock()
	return serpStatus
}

func getUPCItemDBStatus() apiRuntimeStatus {
	upcStatusMu.RLock()
	defer upcStatusMu.RUnlock()
	return upcStatus
}

// sanitizeAPIError strips secrets and truncates for UI display.
func sanitizeAPIError(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Never echo keys if somehow present.
	lower := strings.ToLower(s)
	for _, secret := range []string{"user_key", "api_key", "apikey", "authorization", "bearer "} {
		if idx := strings.Index(lower, secret); idx >= 0 {
			s = s[:idx] + "[redacted]"
			break
		}
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// BuildIntegrationHealth assembles analyst-facing API health for the UI banner.
// serpEnabled/configured and upcEnabled/configured come from config at call time.
func BuildIntegrationHealth(
	snaps []models.DataSnapshot,
	serpEnabled, serpConfigured bool,
	upcEnabled, upcConfigured bool,
) *models.IntegrationHealth {
	var services []models.IntegrationStatus

	// --- PartsBase (from snapshot / last run) ---
	pb := buildPartsBaseStatus(snaps)
	if pb != nil {
		sev := "ok"
		if !pb.OK {
			if !pb.Configured || !pb.Enabled {
				sev = "error"
			} else {
				sev = "error"
			}
		}
		// not_registered / not checked after a run with no snapshot
		msg := pb.Message
		if msg == "" && !pb.OK {
			msg = "PartsBase is not available for this analysis."
		}
		if pb.OK {
			msg = "PartsBase GovData is live."
			sev = "ok"
		}
		services = append(services, models.IntegrationStatus{
			Name:       "PartsBase",
			OK:         pb.OK,
			Enabled:    pb.Enabled,
			Configured: pb.Configured,
			Severity:   sev,
			Message:    msg,
			Error:      pb.Error,
			Detail:     pb.DataSource,
			Live:       pb.Live,
		})
	}

	// --- SerpAPI ---
	services = append(services, serpIntegrationStatus(serpEnabled, serpConfigured))

	// --- UPCItemDB ---
	services = append(services, upcIntegrationStatus(upcEnabled, upcConfigured))

	allOK := true
	hasWarn := false
	for _, s := range services {
		if !s.OK && s.Severity != "info" {
			allOK = false
		}
		if s.Severity == "warning" || s.Severity == "error" {
			hasWarn = true
		}
	}
	return &models.IntegrationHealth{
		AllOK:       allOK,
		HasWarnings: hasWarn,
		Services:    services,
	}
}

func serpIntegrationStatus(enabled, configured bool) models.IntegrationStatus {
	st := models.IntegrationStatus{
		Name:       "SerpAPI",
		Enabled:    enabled,
		Configured: configured,
	}
	rt := getSerpAPIStatus()
	if !enabled {
		st.OK = true
		st.Severity = "info"
		st.Message = "SerpAPI is disabled. Commercial Google Shopping prices will not be enriched."
		return st
	}
	if !configured {
		st.OK = false
		st.Severity = "error"
		st.Message = "SerpAPI is enabled but no API key is loaded. Place IF_SERPAPI_KEY in .env.serpapi (gitignored)."
		return st
	}
	if !rt.Checked {
		st.OK = true // configured; not failed yet
		st.Severity = "info"
		st.Message = "SerpAPI key is loaded. Status will update after the next commercial price resolve."
		return st
	}
	st.CheckedAt = rt.CheckedAt.Format(time.RFC3339)
	if rt.HTTPCode > 0 {
		st.Detail = fmt.Sprintf("http=%d", rt.HTTPCode)
	}
	if rt.OK {
		st.OK = true
		st.Severity = "ok"
		st.Message = nonEmpty(rt.Message, "SerpAPI Google Shopping is responding.")
		st.Live = true
		return st
	}
	st.OK = false
	st.Severity = "error"
	st.Error = rt.Error
	st.Message = nonEmpty(rt.Message, "SerpAPI is not working. Market shopping prices may be incomplete.")
	if rt.HTTPCode == 401 || rt.HTTPCode == 403 {
		st.Message = "SerpAPI rejected the API key (unauthorized). Check IF_SERPAPI_KEY."
		st.Severity = "error"
	} else if rt.HTTPCode == 429 {
		st.Message = "SerpAPI rate limit exceeded. Commercial shopping enrichment may be incomplete."
		st.Severity = "warning"
	}
	return st
}

func upcIntegrationStatus(enabled, configured bool) models.IntegrationStatus {
	st := models.IntegrationStatus{
		Name:       "UPCItemDB",
		Enabled:    enabled,
		Configured: configured,
	}
	rt := getUPCItemDBStatus()
	plan := "trial"
	if configured && UPCItemDBConfigured() {
		plan = "v1"
	}
	st.Detail = "plan=" + plan

	if !enabled {
		st.OK = true
		st.Severity = "info"
		st.Message = "UPCItemDB is disabled. Product identity resolve will skip catalog lookup."
		return st
	}
	// Trial mode without key is allowed (not an error by itself).
	if !configured {
		if !rt.Checked {
			st.OK = true
			st.Severity = "info"
			st.Message = "UPCItemDB is using the free trial endpoint. Paid key not loaded (optional)."
			return st
		}
		// Checked on trial — report runtime failures
	}
	if !rt.Checked {
		st.OK = true
		st.Severity = "ok"
		if configured {
			st.Message = "UPCItemDB paid plan key is loaded (/prod/v1). Status will update after product resolve."
		} else {
			st.Message = "UPCItemDB trial mode is available."
		}
		return st
	}
	st.CheckedAt = rt.CheckedAt.Format(time.RFC3339)
	if rt.HTTPCode > 0 {
		st.Detail = fmt.Sprintf("plan=%s http=%d", plan, rt.HTTPCode)
	}
	if rt.OK {
		st.OK = true
		st.Severity = "ok"
		st.Live = true
		st.Message = nonEmpty(rt.Message, "UPCItemDB is responding.")
		return st
	}
	st.OK = false
	st.Error = rt.Error
	st.Severity = "error"
	st.Message = nonEmpty(rt.Message, "UPCItemDB is not working. Commercial product identity and offers may be incomplete.")
	switch rt.HTTPCode {
	case 401, 403:
		st.Message = "UPCItemDB rejected the API key (unauthorized). Check IF_UPCITEMDB_KEY in .env.upcitemdb."
	case 429:
		st.Message = "UPCItemDB rate limit hit (plan throttle — key is usually still valid). Insight Forge spaces requests; re-run later if commercial links look thin. SerpAPI may still provide market prices."
		st.Severity = "warning"
	}
	return st
}

// IntegrationHealthSnapshot is a lightweight view for /health (no per-run snaps).
func IntegrationHealthSnapshot(serpEnabled, serpConfigured, upcEnabled, upcConfigured bool, partsBase *models.PartsBaseStatus) *models.IntegrationHealth {
	// Empty snaps → PartsBase from explicit status if provided
	var snaps []models.DataSnapshot
	h := BuildIntegrationHealth(snaps, serpEnabled, serpConfigured, upcEnabled, upcConfigured)
	if partsBase != nil && h != nil {
		// Replace default "not registered" PartsBase row with last known
		replaced := false
		for i := range h.Services {
			if h.Services[i].Name == "PartsBase" {
				h.Services[i] = models.IntegrationStatus{
					Name:       "PartsBase",
					OK:         partsBase.OK,
					Enabled:    partsBase.Enabled,
					Configured: partsBase.Configured,
					Severity:   map[bool]string{true: "ok", false: "error"}[partsBase.OK],
					Message:    partsBase.Message,
					Error:      partsBase.Error,
					Detail:     partsBase.DataSource,
					Live:       partsBase.Live,
				}
				if !partsBase.OK && (partsBase.DataSource == "" || partsBase.DataSource == "not_checked") {
					h.Services[i].Severity = "info"
					h.Services[i].OK = true // don't alarm before first query
					h.Services[i].Message = "PartsBase registered; awaiting first analysis query."
				}
				replaced = true
				break
			}
		}
		if !replaced {
			h.Services = append([]models.IntegrationStatus{{
				Name: "PartsBase", OK: partsBase.OK, Enabled: partsBase.Enabled,
				Configured: partsBase.Configured, Message: partsBase.Message, Error: partsBase.Error,
			}}, h.Services...)
		}
		// Recompute flags
		h.AllOK = true
		h.HasWarnings = false
		for _, s := range h.Services {
			if !s.OK && s.Severity != "info" {
				h.AllOK = false
			}
			if s.Severity == "warning" || s.Severity == "error" {
				h.HasWarnings = true
			}
		}
	}
	return h
}
