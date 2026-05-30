package extraction

import (
	"context"
	"math/rand"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// ProgramIntelligenceExtractor provides broader program-level, socio-economic,
// and category context. This is especially valuable for AbilityOne / CNA-style
// analysis where pure catalog and award data is insufficient.
type ProgramIntelligenceExtractor struct{}

func NewProgramIntelligenceExtractor() *ProgramIntelligenceExtractor {
	return &ProgramIntelligenceExtractor{}
}

func (p *ProgramIntelligenceExtractor) SourceCode() string { return "PROGRAM_INTEL" }

func (p *ProgramIntelligenceExtractor) Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error) {
	select {
	case <-time.After(140 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	seed := hashToInt(entityID + "program")
	r := rand.New(rand.NewSource(seed))
	fsc := "0000"
	if len(entityID) >= 4 {
		fsc = entityID[:4]
	}

	programContext, socioEconomicNote, riskNotes := deriveProgramContext(fsc, entityID, r)

	snap := models.DataSnapshot{
		EntityID:   entityID,
		SourceCode: "PROGRAM_INTEL",
		SnapshotAt: time.Now().Add(-time.Duration(r.Intn(120)) * 24 * time.Hour),
		RawResponse: map[string]any{
			"program_family":       programContext,
			"socio_economic_notes": socioEconomicNote,
			"additional_risks":     riskNotes,
			"data_recency":         "Prototype program intelligence layer",
		},
		QualityScore: 0.78 + r.Float64()*0.15,
		CreatedBy:    "program-intel-extractor-v0.1",
	}

	return []models.DataSnapshot{snap}, nil
}

func deriveProgramContext(fsc, entityID string, r *rand.Rand) (program, socio, risks string) {
	switch fsc {
	case "7920", "7520", "8105":
		program = "General federal consumables with strong AbilityOne / Javits-Wagner-O’Day program coverage. Mandatory source considerations frequently apply."
		socio = "High socio-economic return through direct labor hours for blind and significantly disabled workers. Primary production occurs in NIB and SourceAmerica workshops."
		risks = "Key risks include gradual volume erosion from commercial micro-purchase leakage and digital substitution (for office items). Sub-tier visibility is typically limited."
	case "7125":
		program = "Facility modernization and storage equipment. Lower volume, higher unit value than consumables. Often tied to capital projects and base realignment."
		socio = "Still carries AbilityOne socio-economic impact but with higher equipment and skill barriers for producing workshops."
		risks = "Concentration and capacity risk on large projects. Lead times and sub-tier component availability (hardware, finishes) are common concerns."
	case "5180":
		program = "Maintenance and readiness tooling. Often procured in support of field and depot maintenance. Mixed commercial + AbilityOne kitting model."
		socio = "Strong socio-economic multiplier per dollar due to kitting labor, but complicated by significant commercial sub-component content before final assembly."
		risks = "Highest sub-tier exposure among common AbilityOne categories. BOM transparency and dual-sourcing are frequently recommended."
	default:
		program = "Standard federal hardware or sustainment item. Program context varies widely by using activity and appropriation."
		socio = "Socio-economic considerations depend on specific acquisition strategy and any applicable small business or AbilityOne preferences."
		risks = "Typical sustainment risks: supplier health, obsolescence, and surge capacity on short notice."
	}
	return program, socio, risks
}