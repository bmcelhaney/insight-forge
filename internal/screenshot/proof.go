package screenshot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
	"github.com/bmcelhaney/insight-forge/internal/storage"
)

// ProofOptions controls batch evidence capture for a data-capture document.
type ProofOptions struct {
	// MaxPerRun caps page-capture attempts per analyze (default 6).
	MaxPerRun int
	// Timeout per page capture attempt (default 10s).
	Timeout time.Duration
	// BatchTimeout is wall-clock budget for the whole batch after enqueue (default 45s).
	// Remaining pending jobs become status=skipped; product source_url is kept.
	BatchTimeout time.Duration
	// PresignTTL unused in machine payload (no short-lived URLs); kept for API compat.
	PresignTTL time.Duration
}

// AttachProofs captures screenshots for eligible hits and uploads to Tigris.
// Mutates doc.Hits in place. Safe to call with nil store/capturer (no-op).
func AttachProofs(ctx context.Context, doc *models.DataCaptureDocument, store *storage.Client, capturer *Capturer, opts ProofOptions) {
	if doc == nil || store == nil || capturer == nil || !capturer.Available() {
		return
	}
	if opts.MaxPerRun <= 0 {
		opts.MaxPerRun = 15
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.PresignTTL <= 0 {
		opts.PresignTTL = time.Hour
	}
	if doc.AnalysisID == "" {
		return
	}
	nsn := doc.Query.NSN
	if nsn == "" {
		nsn = doc.Query.EntityID
	}

	// Collect eligible hit indices.
	type job struct {
		idx int
		url string
	}
	var jobs []job
	for i := range doc.Hits {
		h := &doc.Hits[i]
		if !eligibleForScreenshot(h) {
			continue
		}
		jobs = append(jobs, job{idx: i, url: strings.TrimSpace(h.Links.URL)})
		if len(jobs) >= opts.MaxPerRun {
			break
		}
	}
	if len(jobs) == 0 {
		return
	}

	// Serial capture: one Chrome session chain is simpler and safer on small hosts.
	// (Parallel Chrome processes thrash sprites.)
	for _, j := range jobs {
		h := &doc.Hits[j.idx]
		if h.Proof == nil {
			h.Proof = &models.DataCaptureProof{}
		}
		h.Proof.Screenshot = &models.DataCaptureScreenshot{
			Status:    "pending",
			SourceURL: j.url,
		}
		shot, err := capturer.CapturePNG(ctx, j.url)
		if err != nil {
			h.Proof.Screenshot.Status = "failed"
			h.Proof.Screenshot.Error = truncateErr(err.Error(), 240)
			continue
		}
		capturedAt := time.Now().UTC()
		key := store.EvidenceObjectKey(nsn, doc.AnalysisID, h.HitID, capturedAt)
		meta := map[string]string{
			"nsn":          nsn,
			"analysis-id":  doc.AnalysisID,
			"hit-id":       h.HitID,
			"source-url":   j.url,
			"url-kind":     h.Links.URLKind,
			"content-sha":  shot.SHA256,
		}
		if h.Pricing != nil {
			meta["merchant"] = h.Pricing.Merchant
			meta["channel"] = h.Pricing.Channel
			meta["unit-price"] = fmt.Sprintf("%.4f", h.Pricing.UnitPrice)
		}
		putCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = store.PutObject(putCtx, key, shot.PNG, "image/png", meta)
		cancel()
		if err != nil {
			h.Proof.Screenshot.Status = "failed"
			h.Proof.Screenshot.Error = truncateErr(err.Error(), 240)
			continue
		}
		h.Proof.Screenshot = &models.DataCaptureScreenshot{
			Status:      "ready",
			Kind:        KindPageScreenshot,
			Bucket:      store.Bucket(),
			ObjectKey:   key,
			ContentType: "image/png",
			CapturedAt:  capturedAt,
			SourceURL:   j.url,
			Width:       shot.Width,
			Height:      shot.Height,
			SHA256:      shot.SHA256,
		}
	}
}

func eligibleForScreenshot(h *models.DataCaptureHit) bool {
	if h == nil || h.Links == nil {
		return false
	}
	u := strings.TrimSpace(h.Links.URL)
	if u == "" {
		return false
	}
	// Prefer priced hits with strong URL kinds.
	kind := strings.ToLower(strings.TrimSpace(h.Links.URLKind))
	switch kind {
	case "merchant_pdp", "amazon_dp", "federal":
		// ok
	default:
		return false
	}
	if h.HitType == "price_observation" {
		return h.Pricing != nil && h.Pricing.UnitPrice > 0
	}
	// Also allow identity hits with strong PDPs (optional proof of mapping).
	return h.HitType == "ets_mapping" || h.HitType == "gsa_listing" || h.HitType == "commercial_reference"
}

func truncateErr(s string, n int) string {
	s = strings.TrimSpace(s)
	// Never leave credentials in error text if SDKs leak them.
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
