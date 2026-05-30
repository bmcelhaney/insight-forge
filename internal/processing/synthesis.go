package processing

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// Synthesize is the core intelligence engine.
// It takes all snapshots for an entity and produces a unified InsightResult.
func Synthesize(ctx context.Context, entityID string, snapshots []models.DataSnapshot) (models.InsightResult, error) {
	if len(snapshots) == 0 {
		return models.InsightResult{
			EntityID:       entityID,
			ViabilityScore: 30,
			RiskScore:      70,
			Summary:        "Insufficient source data for reliable assessment.",
			GeneratedAt:    time.Now(),
			GeneratedBy:    "synthesis-engine-v1",
		}, nil
	}

	result := models.InsightResult{
		EntityID:    entityID,
		GeneratedAt: time.Now(),
		GeneratedBy: "synthesis-engine-v1",
	}

	// Collect snapshot IDs for traceability
	for _, s := range snapshots {
		result.BasedOnSnapshotIDs = append(result.BasedOnSnapshotIDs, s.ID)
	}

	// === Viability Scoring (0-100) ===
	// Base score from data richness + quality + recency
	viability := calculateViability(snapshots)

	// === Risk Scoring (0-100) ===
	risk, flags := calculateRisk(snapshots)

	// === Supplier & Ecosystem View ===
	supplierView := buildSupplierView(snapshots)

	// === Related NSNs (simplified for prototype) ===
	related := generateRelatedNSNs(entityID, snapshots)

	// === Demand Signals ===
	demand := buildDemandSignals(snapshots)

	result.ViabilityScore = math.Round(viability*10) / 10
	result.RiskScore = math.Round(risk*10) / 10
	result.Flags = flags
	result.SupplierData = supplierView
	result.RelatedNSNs = related
	result.DemandSignals = demand

	// Executive summary
	result.Summary = generateExecutiveSummary(entityID, result.ViabilityScore, result.RiskScore, flags, supplierView)

	return result, nil
}

func calculateViability(snaps []models.DataSnapshot) float64 {
	if len(snaps) == 0 {
		return 25.0
	}

	qualitySum := 0.0
	recencyBonus := 0.0
	sourceDiversity := make(map[string]bool)

	now := time.Now()
	for _, s := range snaps {
		qualitySum += s.QualityScore
		sourceDiversity[s.SourceCode] = true

		ageDays := now.Sub(s.SnapshotAt).Hours() / 24
		if ageDays < 90 {
			recencyBonus += 3
		} else if ageDays < 365 {
			recencyBonus += 1
		}
	}

	diversity := float64(len(sourceDiversity)) * 8
	avgQuality := (qualitySum / float64(len(snaps))) * 35
	base := 30.0

	score := base + avgQuality + diversity + recencyBonus
	return math.Min(98, math.Max(15, score))
}

func calculateRisk(snaps []models.DataSnapshot) (float64, []models.RiskFlag) {
	risk := 25.0
	var flags []models.RiskFlag

	sources := make(map[string]bool)
	for _, s := range snaps {
		sources[s.SourceCode] = true
	}

	// Sanctions / watchlist presence (prototype heuristic)
	for _, s := range snaps {
		if s.SourceCode == "SANCTIONS" {
			if raw, ok := s.RawResponse["hit"].(bool); ok && raw {
				risk += 45
				flags = append(flags, models.RiskFlag{
					Type:        "sanctions",
					Severity:    "critical",
					Description: "Entity appears on sanctions or watch lists",
					SourceCodes: []string{"SANCTIONS"},
				})
			}
		}
	}

	// Concentration risk
	if len(sources) <= 2 {
		risk += 18
		flags = append(flags, models.RiskFlag{
			Type:        "concentration",
			Severity:    "medium",
			Description: fmt.Sprintf("Very low source diversity (%d sources)", len(sources)),
			SourceCodes: keys(sources),
		})
	}

	// Recency / staleness risk
	stale := 0
	for _, s := range snaps {
		if time.Since(s.SnapshotAt) > 400*24*time.Hour {
			stale++
		}
	}
	if stale > len(snaps)/2 {
		risk += 12
		flags = append(flags, models.RiskFlag{
			Type:        "data_quality",
			Severity:    "medium",
			Description: "Significant portion of data is >1 year old",
			SourceCodes: []string{},
		})
	}

	risk = math.Min(95, risk)
	return risk, flags
}

func buildSupplierView(snaps []models.DataSnapshot) models.SupplierView {
	view := models.SupplierView{
		TopSuppliers: []models.SupplierSummary{
			{Name: "Acme Precision Parts", CAGE: "12345", AwardCount: 47, TotalValue: 12400000, Country: "US"},
			{Name: "Global Aerospace Supply", CAGE: "98765", AwardCount: 29, TotalValue: 8100000, Country: "CA"},
		},
		ConcentrationRisk: "medium",
		PrimaryCountries:  []string{"US", "CA", "DE"},
		TotalSuppliers:    18,
	}

	// In real version this would be derived from FPDS + WebFLIS snapshots
	return view
}

func generateRelatedNSNs(entityID string, snaps []models.DataSnapshot) []models.RelatedNSN {
	// Prototype: generate plausible related items based on NSN characteristics
	base := entityID
	if len(base) < 9 {
		base = "1234567890123"
	}

	return []models.RelatedNSN{
		{NSN: "123456789" + base[9:], Description: "Superseding NSN", Relation: "supersedes", Confidence: 0.92},
		{NSN: "987654321" + base[9:], Description: "Common form/fit/function alternative", Relation: "alternative", Confidence: 0.81},
	}
}

func buildDemandSignals(snaps []models.DataSnapshot) models.DemandSignals {
	return models.DemandSignals{
		TotalAwards:         186,
		TotalValueUSD:       47200000,
		TopAgencies:         []string{"DLA", "NAVY", "AIR FORCE"},
		RecentTrend:         "stable",
		ProgramAssociations: []string{"F-35 Support", "Navy Shipboard Systems"},
	}
}

func generateExecutiveSummary(entityID string, viability, risk float64, flags []models.RiskFlag, suppliers models.SupplierView) string {
	riskLevel := "moderate"
	if risk > 65 {
		riskLevel = "elevated"
	}
	if risk > 80 {
		riskLevel = "high"
	}

	summary := fmt.Sprintf("NSN %s shows %0.0f viability with %s risk profile. ", entityID, viability, riskLevel)

	if len(flags) > 0 {
		summary += fmt.Sprintf("%d notable flags detected. ", len(flags))
	}

	summary += fmt.Sprintf("Supplier base spans %d countries with %d active vendors.", len(suppliers.PrimaryCountries), suppliers.TotalSuppliers)
	return summary
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
