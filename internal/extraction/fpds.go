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

	snap := models.DataSnapshot{
		EntityID:    entityID,
		SourceCode:  "FPDS",
		SnapshotAt:  now.Add(-time.Duration(r.Intn(400)) * 24 * time.Hour),
		RawResponse: map[string]any{
			"total_awards":     40 + r.Intn(180),
			"total_value_usd":  1200000 + r.Int63n(48000000),
			"top_agencies":     []string{"DLA", "NAVY", "AIR FORCE", "ARMY"},
			"last_award_date":  now.Add(-time.Duration(r.Intn(120)) * 24 * time.Hour).Format(time.RFC3339),
			"note":             "Prototype data — replace with real SAM.gov FPDS API call",
		},
		QualityScore: 0.82 + r.Float64()*0.12,
		CreatedBy:    "fpds-extractor-v0.9",
	}

	// Occasionally mark as outlier for demo
	if r.Intn(12) == 0 {
		snap.IsOutlier = true
		snap.QualityScore *= 0.6
	}

	return []models.DataSnapshot{snap}, nil
}

func hashToInt(s string) int64 {
	h := sha256.Sum256([]byte(s))
	hexStr := hex.EncodeToString(h[:8])
	var val int64
	fmt.Sscanf(hexStr, "%x", &val)
	return val
}
