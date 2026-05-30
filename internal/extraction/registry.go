package extraction

import (
	"context"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// Registry holds all active extractors for an Insight Forge instance.
type Registry struct {
	extractors map[string]Extractor
}

// NewDefaultRegistry returns the standard set of extractors for Insight Forge.
func NewDefaultRegistry() *Registry {
	r := &Registry{
		extractors: make(map[string]Extractor),
	}

	// Register core sources
	w := NewWebFLISExtractor()
	r.extractors[w.SourceCode()] = w

	f := NewFPDSExtractor()
	r.extractors[f.SourceCode()] = f

	s := NewSanctionsExtractor()
	r.extractors[s.SourceCode()] = s

	// New wider-search extractor for program/socio-economic context (especially valuable for AbilityOne and CNA-style work)
	pi := NewProgramIntelligenceExtractor()
	r.extractors[pi.SourceCode()] = pi

	// Future: MCRL, SAM.gov, historical award feeds, technical manuals, etc.
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
