package extraction

import (
	"context"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// Extractor is the contract all data sources must implement.
// See Stitchify Go Framework §4 for full rules.
type Extractor interface {
	// Fetch returns immutable snapshots for the given entity (NSN or partial identifier).
	Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error)

	// SourceCode returns the stable identifier used in data_sources.code (e.g. "WEBFLIS").
	SourceCode() string
}
