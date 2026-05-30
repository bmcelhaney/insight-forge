package extraction

import (
	"context"
	"math/rand"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// TechnicalContextExtractor provides additional technical, regulatory, and
// maintenance context. This gives the general synthesis path more dimensions
// to reason over for arbitrary NSNs, helping the output feel more multi-source
// and less canned.
type TechnicalContextExtractor struct{}

func NewTechnicalContextExtractor() *TechnicalContextExtractor {
	return &TechnicalContextExtractor{}
}

func (t *TechnicalContextExtractor) SourceCode() string { return "TECH_CONTEXT" }

func (t *TechnicalContextExtractor) Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error) {
	select {
	case <-time.After(110 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	seed := hashToInt(entityID + "tech")
	r := rand.New(rand.NewSource(seed))
	fsc := "0000"
	if len(entityID) >= 4 {
		fsc = entityID[:4]
	}

	techNotes, regNotes, maintNotes := deriveTechnicalContext(fsc, entityID, r)

	snap := models.DataSnapshot{
		EntityID:   entityID,
		SourceCode: "TECH_CONTEXT",
		SnapshotAt: time.Now().Add(-time.Duration(r.Intn(200)) * 24 * time.Hour),
		RawResponse: map[string]any{
			"technical_notes":     techNotes,
			"regulatory_notes":    regNotes,
			"maintenance_notes":   maintNotes,
			"data_quality_note":   "Prototype technical context layer",
		},
		QualityScore: 0.75 + r.Float64()*0.18,
		CreatedBy:    "tech-context-extractor-v0.1",
	}

	return []models.DataSnapshot{snap}, nil
}

func deriveTechnicalContext(fsc, entityID string, r *rand.Rand) (tech, reg, maint string) {
	switch fsc {
	case "7220", "7210", "7230": // Floor coverings / furnishings
		tech = "Products in this category typically meet commercial performance standards for durability, flame resistance, and slip resistance in high-traffic federal facilities."
		reg = "May be subject to GSA or agency-specific sustainability and indoor air quality requirements. Some items carry recycled content preferences."
		maint = "Routine cleaning and periodic replacement driven by wear patterns. Lifecycle often 3-7 years depending on traffic and maintenance quality."
	case "7920", "7930", "8540": // Cleaning and paper products
		tech = "Industrial/commercial grade consumables with standardized performance characteristics for absorbency, strength, and chemical compatibility."
		reg = "Generally low regulatory burden. Some products may reference EPA Safer Choice or other environmental preference programs."
		maint = "High-turn consumables with predictable reorder cycles. Inventory management is the primary operational consideration."
	default:
		tech = "Technical characteristics vary by exact item configuration. Prototype data provides category-level context only."
		reg = "Regulatory and compliance requirements are item-specific. Full evaluation requires access to the actual technical data package."
		maint = "Maintenance and sustainment approach depends on the using activity's operational tempo and approved parts strategy."
	}
	return tech, reg, maint
}