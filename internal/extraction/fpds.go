package extraction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// FPDSExtractor pulls from Federal Procurement Data System (public data).
// For the prototype we use deterministic realistic mock data derived from the NSN.
// Real implementation can call https://api.sam.gov/prod/federalprocurement/v1/ with an API key.
type FPDSExtractor struct{}

func NewFPDSExtractor() *FPDSExtractor {
	return &FPDSExtractor{}
}

func (f *FPDSExtractor) SourceCode() string { return "FPDS" }

func (f *FPDSExtractor) Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error) {
	// Simulate network + processing time
	select {
	case <-time.After(180 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	seed := hashToInt(entityID + "fpds")
	r := rand.New(rand.NewSource(seed))

	now := time.Now()
	fsc := "0000"
	if len(entityID) >= 4 {
		fsc = entityID[:4]
	}

	totalAwards, totalValue, topAgencies, lastAward, demandNote := deriveFPDSPattern(fsc, entityID, r)

	snap := models.DataSnapshot{
		EntityID:   entityID,
		SourceCode: "FPDS",
		SnapshotAt: now.Add(-time.Duration(r.Intn(400)) * 24 * time.Hour),
		RawResponse: map[string]any{
			"total_awards":    totalAwards,
			"total_value_usd": totalValue,
			"top_agencies":    topAgencies,
			"last_award_date": lastAward,
			"demand_character": demandNote,
			"note":            "Prototype FPDS data — category-sensitive award patterns for demo",
		},
		QualityScore: 0.82 + r.Float64()*0.12,
		CreatedBy:    "fpds-extractor-v1.1",
	}

	// Occasionally mark as outlier for demo
	if r.Intn(12) == 0 {
		snap.IsOutlier = true
		snap.QualityScore *= 0.6
	}

	return []models.DataSnapshot{snap}, nil
}

// deriveFPDSPattern produces believable, category-differentiated federal award data.
// This prevents the "same canned aerospace numbers for every NSN" problem.
func deriveFPDSPattern(fsc, entityID string, r *rand.Rand) (totalAwards int, totalValue int64, topAgencies []string, lastAward, demandNote string) {
	now := time.Now()
	lastAward = now.Add(-time.Duration(r.Intn(90)) * 24 * time.Hour).Format(time.RFC3339)

	switch fsc {
	case "7920", "7520", "8105": // AbilityOne-style consumables / office / packaging
		totalAwards = 80 + r.Intn(280)
		totalValue = 800000 + r.Int63n(3200000)
		topAgencies = []string{"DLA Troop Support", "GSA", "VA", "Army"}
		demandNote = "Steady high-volume consumable with seasonal and year-end surge patterns"
	case "7125": // Shelving / storage (project-driven)
		totalAwards = 18 + r.Intn(45)
		totalValue = 1400000 + r.Int63n(3800000)
		topAgencies = []string{"VA", "Air Force", "Army Corps of Engineers", "GSA"}
		demandNote = "Lumpy, project-tied demand tied to facility modernization and new construction"
	case "5180": // Tool kits (lumpy, maintenance)
		totalAwards = 12 + r.Intn(32)
		totalValue = 900000 + r.Int63n(2100000)
		topAgencies = []string{"DLA", "Navy", "Air Force", "Marine Corps"}
		demandNote = "Irregular, large-order driven demand linked to maintenance and tool refresh cycles"
	default:
		totalAwards = 25 + r.Intn(120)
		totalValue = 1800000 + r.Int63n(12000000)
		topAgencies = []string{"DLA", "NAVY", "AIR FORCE", "ARMY"}
		demandNote = "Mixed sustainment and project demand typical of federal hardware"
	}
	return totalAwards, totalValue, topAgencies, lastAward, demandNote
}

func hashToInt(s string) int64 {
	h := sha256.Sum256([]byte(s))
	hexStr := hex.EncodeToString(h[:8])
	var val int64
	fmt.Sscanf(hexStr, "%x", &val)
	return val
}
