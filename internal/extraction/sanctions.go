package extraction

import (
	"context"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// SanctionsExtractor checks against consolidated sanctions / watch lists.
// For prototype: uses a simple heuristic so certain NSNs or suffixes trigger hits.
// Real version would download latest OFAC SDN + other lists and do fuzzy matching.
type SanctionsExtractor struct{}

func NewSanctionsExtractor() *SanctionsExtractor {
	return &SanctionsExtractor{}
}

func (s *SanctionsExtractor) SourceCode() string { return "SANCTIONS" }

func (s *SanctionsExtractor) Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error) {
	select {
	case <-time.After(90 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	now := time.Now()

	// Demo heuristic: NSNs ending in 0001, 7777, or containing "999" get a hit
	hasHit := false
	if len(entityID) >= 4 {
		suffix := entityID[len(entityID)-4:]
		if suffix == "0001" || suffix == "7777" || contains(entityID, "999") {
			hasHit = true
		}
	}

	raw := map[string]any{
		"lists_checked": []string{"OFAC SDN", "BIS Entity List", "UN Consolidated"},
		"hit":           hasHit,
		"match_confidence": 0.0,
		"note":          "Prototype implementation — replace with real list ingestion + fuzzy matching",
	}
	if hasHit {
		raw["match_confidence"] = 0.87
		raw["matched_names"] = []string{"Example Restricted Entity LLC"}
	}

	snap := models.DataSnapshot{
		EntityID:     entityID,
		SourceCode:   "SANCTIONS",
		SnapshotAt:   now,
		RawResponse:  raw,
		QualityScore: 0.95,
		CreatedBy:    "sanctions-extractor-v0.8",
	}

	return []models.DataSnapshot{snap}, nil
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsHelper(s, sub))
}

func containsHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
