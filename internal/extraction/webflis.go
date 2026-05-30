package extraction

import (
	"context"
	"fmt"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// WebFLISExtractor is a stub implementation.
// Real implementation will call public WebFLIS / PUB LOG endpoints or scrape as appropriate.
type WebFLISExtractor struct{}

func NewWebFLISExtractor() *WebFLISExtractor {
	return &WebFLISExtractor{}
}

func (w *WebFLISExtractor) SourceCode() string {
	return "WEBFLIS"
}

func (w *WebFLISExtractor) Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error) {
	// TODO: Implement real fetch against WebFLIS / PUB LOG
	// Must:
	//   - Respect rate limits + backoff
	//   - Store raw_response
	//   - Set snapshot_at to capture time
	//   - Compute quality_score
	return nil, fmt.Errorf("WebFLISExtractor.Fetch not yet implemented for entity %s", entityID)
}
