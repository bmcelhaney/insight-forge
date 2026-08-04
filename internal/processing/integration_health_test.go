package processing

import (
	"testing"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

func TestBuildIntegrationHealth_MissingSerpKey(t *testing.T) {
	ConfigureIntegrationFlags(true, false, true, false)
	h := BuildIntegrationHealth(nil, true, false, true, false)
	if h == nil || !h.HasWarnings {
		t.Fatal("expected warnings when Serp key missing")
	}
	var found bool
	for _, s := range h.Services {
		if s.Name == "SerpAPI" {
			found = true
			if s.OK {
				t.Fatal("SerpAPI should not be OK without key")
			}
			if s.Severity != "error" {
				t.Fatalf("severity: %s", s.Severity)
			}
		}
	}
	if !found {
		t.Fatal("missing SerpAPI service row")
	}
}

func TestRecordUPCItemDBUnauthorized(t *testing.T) {
	recordUPCItemDBStatus(false, 401, "HTTP 401", "UPCItemDB rejected the API key")
	st := upcIntegrationStatus(true, true)
	if st.OK {
		t.Fatal("expected not OK after 401")
	}
	if st.Severity != "error" {
		t.Fatalf("severity %s", st.Severity)
	}
}

func TestIntegrationHealthSnapshot_PartsBaseNotChecked(t *testing.T) {
	pb := &models.PartsBaseStatus{
		OK: true, Enabled: true, Configured: true,
		DataSource: "not_checked",
		Message:    "awaiting",
	}
	h := IntegrationHealthSnapshot(true, true, true, true, pb)
	if h == nil {
		t.Fatal("nil health")
	}
	for _, s := range h.Services {
		if s.Name == "PartsBase" && s.Severity == "error" {
			t.Fatal("should not error before first PartsBase query")
		}
	}
}
