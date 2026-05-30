package extraction

import (
	"context"
	"math/rand"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// WebFLISExtractor queries public WebFLIS / PUB LOG data for NSN characteristics,
// pricing history, packaging, unit of issue, etc.
// Prototype uses rich deterministic mocks. Real version would integrate with
// official DLA WebFLIS or authorized data services.
type WebFLISExtractor struct{}

func NewWebFLISExtractor() *WebFLISExtractor {
	return &WebFLISExtractor{}
}

func (w *WebFLISExtractor) SourceCode() string { return "WEBFLIS" }

func (w *WebFLISExtractor) Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error) {
	select {
	case <-time.After(220 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	seed := hashToInt(entityID + "webflis")
	r := rand.New(rand.NewSource(seed))
	now := time.Now()

	snap := models.DataSnapshot{
		EntityID:   entityID,
		SourceCode: "WEBFLIS",
		SnapshotAt: now.Add(-time.Duration(r.Intn(180)) * 24 * time.Hour),
		RawResponse: map[string]any{
			"niin":             entityID,
			"fsc":              "1680",
			"item_name":        "VALVE ASSEMBLY, HYDRAULIC",
			"unit_of_issue":    "EA",
			"unit_price":       1240 + r.Intn(3800),
			"packaging":        "MIL-STD-2073",
			"technical_characteristics": "Aluminum body, 3000 PSI, -65F to +275F",
			"last_updated":     now.Add(-time.Duration(r.Intn(90)) * 24 * time.Hour).Format("2006-01-02"),
			"note":             "Prototype data — integrate real WebFLIS/PUB LOG feed",
		},
		QualityScore: 0.88 + r.Float64()*0.09,
		CreatedBy:    "webflis-extractor-v1.1",
	}

	return []models.DataSnapshot{snap}, nil
}
