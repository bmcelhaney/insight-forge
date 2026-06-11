package extraction

import (
	"context"
	"os"
	"strings"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// Registry holds all active extractors for an Insight Forge instance.
type Registry struct {
	extractors map[string]Extractor
}

// NewDefaultRegistry returns the standard set of extractors for Insight Forge.
// Pass a non-empty samAPIKey to enable SAM-backed FPDS behavior.
// PartsBase is registered when enabled and configured with a key.
func NewDefaultRegistry(samAPIKey string, partsBaseCfg PartsBaseConfig) *Registry {
	r := &Registry{
		extractors: make(map[string]Extractor),
	}

	// Register core sources
	w := NewWebFLISExtractor()
	r.extractors[w.SourceCode()] = w

	f := NewFPDSExtractor(samAPIKey)
	r.extractors[f.SourceCode()] = f

	s := NewSanctionsExtractor()
	r.extractors[s.SourceCode()] = s

	// New wider-search extractor for program/socio-economic context (especially valuable for AbilityOne and CNA-style work)
	pi := NewProgramIntelligenceExtractor()
	r.extractors[pi.SourceCode()] = pi

	// Additional technical/regulatory/maintenance context to give the general path more dimensions for arbitrary NSNs
	tc := NewTechnicalContextExtractor()
	r.extractors[tc.SourceCode()] = tc

	// GSA Advantage web scraping for real AbilityOne (JWOD) pricing - direct POST + HTML scrape as specified
	gsa := NewGSAAdvantageExtractor()
	r.extractors[gsa.SourceCode()] = gsa

	// Dedicated AbilityOne program data (mandatory source status, real NPA/CAGE, CID, MPL pricing notes, demand character).
	// Critical upgrade for the general path on the large volume of AbilityOne-relevant NSNs.
	ao := NewAbilityOneExtractor()
	r.extractors[ao.SourceCode()] = ao

	// AbilityOne ETS spreadsheet cross-reference data (SKU/UPC/manufacturer mappings).
	// This enriches NSN analysis with additional commercial identifiers and descriptions.
	etsPath := strings.TrimSpace(os.Getenv("IF_ETS_XLSX_PATH"))
	ets := NewAbilityOneETSExtractor(etsPath)
	r.extractors[ets.SourceCode()] = ets

	// Live web-search intelligence layer for deeper non-demo NSN insights.
	wi := NewWebSearchIntelExtractor()
	r.extractors[wi.SourceCode()] = wi

	// PartsBase market-pricing intelligence (feature-toggled, key-gated).
	if partsBaseCfg.Enabled && strings.TrimSpace(partsBaseCfg.APIKey) != "" {
		pb := NewPartsBaseExtractor(partsBaseCfg)
		r.extractors[pb.SourceCode()] = pb
	}

	// Future: MCRL, SAM.gov, historical award feeds, technical manuals, bulk PUB LOG integration, etc.
	return r
}

// FetchAll runs all (or selected) extractors in parallel for the given entity.
func (r *Registry) FetchAll(ctx context.Context, entityID string, sources []string, params map[string]string) ([]models.DataSnapshot, error) {
	type result struct {
		snaps []models.DataSnapshot
		err   error
	}

	results := make(chan result, len(r.extractors))

	targets := sources
	if len(targets) == 0 {
		for code := range r.extractors {
			targets = append(targets, code)
		}
	}

	for _, code := range targets {
		ex, ok := r.extractors[code]
		if !ok {
			continue
		}
		go func(e Extractor) {
			snaps, err := e.Fetch(ctx, entityID, params)
			results <- result{snaps: snaps, err: err}
		}(ex)
	}

	var all []models.DataSnapshot
	for i := 0; i < len(targets); i++ {
		res := <-results
		if res.err != nil {
			// In production we would log and continue (partial results are valuable)
			continue
		}
		all = append(all, res.snaps...)
	}
	return all, nil
}
