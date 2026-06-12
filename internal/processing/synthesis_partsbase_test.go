package processing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

func TestCollectScoringEvidenceIncludesPartsBase(t *testing.T) {
	t.Parallel()

	snaps := []models.DataSnapshot{
		{
			EntityID:   "8415016107327",
			SourceCode: "PARTSBASE",
			SnapshotAt: time.Now(),
			RawResponse: map[string]any{
				"result_count":   6,
				"supplier_count": 3,
				"suppliers":      []string{"Vendor A", "Vendor B", "Vendor C"},
				"price_signals": []map[string]any{
					{"unit_price": 10.5},
				},
			},
		},
	}

	e := collectScoringEvidence(snaps)
	if !e.HasPartsBase {
		t.Fatalf("expected HasPartsBase=true")
	}
	if e.PartsBaseResultCount != 6 {
		t.Fatalf("expected PartsBaseResultCount=6, got %d", e.PartsBaseResultCount)
	}
	if e.PartsBaseSupplierCount != 3 {
		t.Fatalf("expected PartsBaseSupplierCount=3, got %d", e.PartsBaseSupplierCount)
	}
	if e.LiveSignalCount != 1 {
		t.Fatalf("expected LiveSignalCount=1 with PartsBase-only evidence, got %d", e.LiveSignalCount)
	}
}

func TestPartsBaseEvidenceInfluencesViabilityAndRisk(t *testing.T) {
	t.Parallel()

	base := []models.DataSnapshot{
		{
			EntityID:   "9999000011111",
			SourceCode: "FPDS",
			SnapshotAt: time.Now(),
			RawResponse: map[string]any{
				"data_source":   "live_usaspending",
				"total_awards":  12,
				"total_value_usd": 240000,
			},
		},
	}

	withPartsBase := append([]models.DataSnapshot{}, base...)
	withPartsBase = append(withPartsBase, models.DataSnapshot{
		EntityID:   "9999000011111",
		SourceCode: "PARTSBASE",
		SnapshotAt: time.Now(),
		RawResponse: map[string]any{
			"result_count":   8,
			"supplier_count": 4,
			"suppliers":      []string{"A", "B", "C", "D"},
			"price_signals": []map[string]any{
				{"condition_code": "NE", "unit_price": 12.3},
			},
		},
	})

	viabilityBase := calculateViabilityFromEvidence(base)
	riskBase, _ := calculateRiskFromEvidence(base)
	viabilityWithPartsBase := calculateViabilityFromEvidence(withPartsBase)
	riskWithPartsBase, _ := calculateRiskFromEvidence(withPartsBase)

	if viabilityWithPartsBase <= viabilityBase {
		t.Fatalf("expected viability to improve with PartsBase evidence (base=%f, withPartsBase=%f)", viabilityBase, viabilityWithPartsBase)
	}
	if riskWithPartsBase >= riskBase {
		t.Fatalf("expected risk to improve (decrease) with PartsBase evidence (base=%f, withPartsBase=%f)", riskBase, riskWithPartsBase)
	}
}

func TestSynthesizeAddsPartsBaseCitationAndNarrative(t *testing.T) {
	t.Parallel()

	now := time.Now()
	snaps := []models.DataSnapshot{
		{
			EntityID:   "9999000011111",
			SourceCode: "WEBFLIS",
			SnapshotAt: now,
			RawResponse: map[string]any{
				"item_name":                 "TEST PART",
				"unit_of_issue":             "EA",
				"technical_characteristics": "Test characteristics",
			},
			QualityScore: 0.9,
		},
		{
			EntityID:   "9999000011111",
			SourceCode: "FPDS",
			SnapshotAt: now,
			RawResponse: map[string]any{
				"data_source":      "live_usaspending",
				"total_awards":     14,
				"total_value_usd":  640000,
				"top_agencies":     []string{"DLA"},
				"demand_character": "Live demand signal",
			},
			QualityScore: 0.9,
		},
		{
			EntityID:   "9999000011111",
			SourceCode: "PARTSBASE",
			SnapshotAt: now,
			RawResponse: map[string]any{
				"result_count":   4,
				"supplier_count": 2,
				"suppliers":      []string{"Vendor A", "Vendor B"},
				"price_signals": []map[string]any{
					{"condition_code": "NE", "unit_price": 10.4},
				},
				"commercial_references": []map[string]any{
					{"sku": "PN-123", "manufacturer": "Acme", "price": "10.40"},
				},
			},
			QualityScore: 0.9,
		},
	}

	result, err := Synthesize(context.Background(), "9999000011111", snaps)
	if err != nil {
		t.Fatalf("synthesize returned error: %v", err)
	}

	if !stringSliceContains(result.Citations, "PartsBase GovData API (live)") {
		t.Fatalf("expected PartsBase citation, got: %#v", result.Citations)
	}
	if !strings.Contains(result.MarketCommentary, "PartsBase GovData contributed") {
		t.Fatalf("expected market commentary to mention PartsBase contribution, got: %q", result.MarketCommentary)
	}
	if !insightsContainSubstring(result.KeyInsights, "PartsBase") {
		t.Fatalf("expected key insights to include PartsBase reference, got: %#v", result.KeyInsights)
	}
}

func TestSynthesizeUsesPartsBaseFallbackWhenUSASpendingUnavailable(t *testing.T) {
	t.Parallel()

	now := time.Now()
	snaps := []models.DataSnapshot{
		{
			EntityID:   "7420014844559",
			SourceCode: "WEBFLIS",
			SnapshotAt: now,
			RawResponse: map[string]any{
				"item_name":                 "PORTABLE COMPUTER ACCESSORY",
				"unit_of_issue":             "EA",
				"technical_characteristics": "Test technical profile",
			},
			QualityScore: 0.9,
		},
		{
			EntityID:   "7420014844559",
			SourceCode: "FPDS",
			SnapshotAt: now,
			RawResponse: map[string]any{
				"data_source": "prototype",
			},
			QualityScore: 0.4,
		},
		{
			EntityID:   "7420014844559",
			SourceCode: "PARTSBASE",
			SnapshotAt: now,
			RawResponse: map[string]any{
				"data_source":    "live_partsbase_govdata",
				"result_count":   1855,
				"supplier_count": 4,
				"suppliers":      []string{"Alpha Supply", "Bravo Tools", "Charlie Federal", "Delta Logistics"},
				"last_updated":   "2026-06-12",
				"price_signals": []map[string]any{
					{"supplier": "Alpha Supply", "unit_price": 24.5, "quantity": 10, "award_date": "2026-05-01"},
					{"supplier": "Alpha Supply", "unit_price": 22.1, "quantity": 4, "award_date": "2026-04-15"},
					{"supplier": "Bravo Tools", "unit_price": 31.2, "quantity": 3, "award_date": "2026-03-08"},
					{"supplier": "Charlie Federal", "unit_price": 18.9, "quantity": 8, "award_date": "2026-02-20"},
				},
			},
			QualityScore: 0.9,
		},
	}

	result, err := Synthesize(context.Background(), "7420014844559", snaps)
	if err != nil {
		t.Fatalf("synthesize returned error: %v", err)
	}

	if result.DemandSignals.TotalAwards != 1855 {
		t.Fatalf("expected PartsBase fallback demand count 1855, got %d", result.DemandSignals.TotalAwards)
	}
	if !isPartsBaseDemandFallback(result.DemandSignals) {
		t.Fatalf("expected demand signals to be marked as PartsBase fallback: %#v", result.DemandSignals.ProgramAssociations)
	}
	if result.SupplierData.TotalSuppliers != 4 {
		t.Fatalf("expected supplier fallback count 4, got %d", result.SupplierData.TotalSuppliers)
	}
	if !strings.Contains(result.SupplierData.EcosystemNote, "PartsBase") {
		t.Fatalf("expected supplier ecosystem note to mention PartsBase, got: %q", result.SupplierData.EcosystemNote)
	}
	if !insightsContainSubstring(result.KeyInsights, "PartsBase GovData surfaced 1855") {
		t.Fatalf("expected key insights to include PartsBase demand baseline, got: %#v", result.KeyInsights)
	}
	for _, insight := range result.KeyInsights {
		if strings.Contains(insight, "No live USAspending award totals were returned in this run, so demand trends should be treated as lower confidence until refreshed.") {
			t.Fatalf("found stale contradictory USAspending-only insight: %q", insight)
		}
	}
	if !flagsContainSubstringWithSeverity(result.Flags, "USAspending award rows were not returned", "medium") {
		t.Fatalf("expected medium-severity USAspending-missing flag with PartsBase mitigation, got: %#v", result.Flags)
	}
}

func TestCalculateRiskFromEvidenceDowngradesMissingUSASpendingWhenPartsBasePresent(t *testing.T) {
	t.Parallel()

	snaps := []models.DataSnapshot{
		{
			EntityID:   "7420014844559",
			SourceCode: "FPDS",
			SnapshotAt: time.Now(),
			RawResponse: map[string]any{
				"data_source": "prototype",
			},
		},
		{
			EntityID:   "7420014844559",
			SourceCode: "PARTSBASE",
			SnapshotAt: time.Now(),
			RawResponse: map[string]any{
				"result_count":   250,
				"supplier_count": 6,
				"suppliers":      []string{"A", "B", "C", "D", "E", "F"},
				"price_signals":  []map[string]any{{"supplier": "A", "unit_price": 10.0}},
			},
		},
	}

	_, flags := calculateRiskFromEvidence(snaps)
	if !flagsContainSubstringWithSeverity(flags, "USAspending award rows were not returned", "medium") {
		t.Fatalf("expected medium severity missing-USAspending flag when PartsBase evidence exists, got: %#v", flags)
	}
	if flagsContainSubstringWithSeverity(flags, "USAspending award rows were not returned", "high") {
		t.Fatalf("did not expect high severity missing-USAspending flag when PartsBase evidence exists, got: %#v", flags)
	}
}

func stringSliceContains(values []string, needle string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == needle {
			return true
		}
	}
	return false
}

func insightsContainSubstring(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func flagsContainSubstringWithSeverity(flags []models.RiskFlag, needle, severity string) bool {
	for _, flag := range flags {
		if strings.EqualFold(strings.TrimSpace(flag.Severity), strings.TrimSpace(severity)) &&
			strings.Contains(flag.Description, needle) {
			return true
		}
	}
	return false
}
