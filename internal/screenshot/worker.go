package screenshot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
	"github.com/bmcelhaney/insight-forge/internal/storage"
)

// HitLabel is lightweight UI metadata for a proof row.
type HitLabel struct {
	HitID       string  `json:"hit_id"`
	HitType     string  `json:"hit_type,omitempty"`
	SKU         string  `json:"sku,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Merchant    string  `json:"merchant,omitempty"`
	UnitPrice   float64 `json:"unit_price,omitempty"`
	Channel     string  `json:"channel,omitempty"`
	URLKind     string  `json:"url_kind,omitempty"`
	SourceURL   string  `json:"source_url,omitempty"`
}

// RunStatus is the pollable state of an analysis screenshot batch.
type RunStatus struct {
	AnalysisID string                                `json:"analysis_id"`
	NSN        string                                `json:"nsn,omitempty"`
	Status     string                                `json:"status"` // pending | running | complete
	Total      int                                   `json:"total"`
	Done       int                                   `json:"done"`
	Ready      int                                   `json:"ready"`
	Failed     int                                   `json:"failed"`
	Hits       map[string]*models.DataCaptureScreenshot `json:"hits"` // hit_id → screenshot
	Labels     map[string]HitLabel                   `json:"labels,omitempty"`
	UpdatedAt  time.Time                             `json:"updated_at"`
}

type captureJob struct {
	analysisID      string
	nsn             string
	hitID           string
	pageURL         string
	productImageURL string // SerpAPI thumbnail etc. — used when page is bot-walled
	urlKind         string
	label           HitLabel
	timeout         time.Duration
	presignTTL      time.Duration
}

// Worker runs screenshot jobs in the background and stores results by analysis_id.
type Worker struct {
	store    *storage.Client
	capturer *Capturer
	opts     ProofOptions

	mu   sync.RWMutex
	runs map[string]*RunStatus

	jobs chan captureJob
	once sync.Once
}

// NewWorker creates a background screenshot worker. start() is called on first enqueue.
func NewWorker(store *storage.Client, capturer *Capturer, opts ProofOptions) *Worker {
	if opts.MaxPerRun <= 0 {
		opts.MaxPerRun = 15
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.PresignTTL <= 0 {
		opts.PresignTTL = time.Hour
	}
	return &Worker{
		store:    store,
		capturer: capturer,
		opts:     opts,
		runs:     map[string]*RunStatus{},
		jobs:     make(chan captureJob, 256),
	}
}

// Available is true when store + chrome are ready.
func (w *Worker) Available() bool {
	return w != nil && w.store != nil && w.capturer != nil && w.capturer.Available()
}

// MarkPendingAndEnqueue sets proof.screenshot.status=pending on eligible hits and
// enqueues async capture jobs. Does not block on browser work.
func (w *Worker) MarkPendingAndEnqueue(doc *models.DataCaptureDocument) int {
	if !w.Available() || doc == nil || doc.AnalysisID == "" {
		return 0
	}
	w.once.Do(func() {
		n := 1
		if w.capturer != nil {
			n = w.capturer.Parallelism()
		}
		if n < 1 {
			n = 1
		}
		if n > 6 {
			n = 6
		}
		for i := 0; i < n; i++ {
			go w.loop()
		}
	})

	nsn := doc.Query.NSN
	if nsn == "" {
		nsn = doc.Query.EntityID
	}

	run := &RunStatus{
		AnalysisID: doc.AnalysisID,
		NSN:        nsn,
		Status:     "pending",
		Hits:       map[string]*models.DataCaptureScreenshot{},
		Labels:     map[string]HitLabel{},
		UpdatedAt:  time.Now().UTC(),
	}

	queued := 0
	for i := range doc.Hits {
		h := &doc.Hits[i]
		if !eligibleForScreenshot(h) {
			continue
		}
		if queued >= w.opts.MaxPerRun {
			break
		}
		src := strings.TrimSpace(h.Links.URL)
		productImg := ""
		if h.Attributes != nil {
			if v, ok := h.Attributes["product_image"].(string); ok {
				productImg = strings.TrimSpace(v)
			}
		}
		label := HitLabel{
			HitID:        h.HitID,
			HitType:      h.HitType,
			SKU:          h.Identifiers.SKU,
			Manufacturer: h.Identifiers.Manufacturer,
			URLKind:      h.Links.URLKind,
			SourceURL:    src,
		}
		if h.Pricing != nil {
			label.Merchant = h.Pricing.Merchant
			label.UnitPrice = h.Pricing.UnitPrice
			label.Channel = h.Pricing.Channel
		}
		// Anticipate kind for UI while pending.
		pendingKind := "page_screenshot"
		if isBotWalledHost(hostOf(src)) {
			if productImg != "" {
				pendingKind = "product_image"
			} else {
				pendingKind = "product_image" // may still fail without image
			}
		}
		pending := &models.DataCaptureScreenshot{
			Status:    "pending",
			Kind:      pendingKind,
			SourceURL: src,
		}
		if h.Proof == nil {
			h.Proof = &models.DataCaptureProof{}
		}
		h.Proof.Screenshot = pending

		// Store snapshot for polling (copy).
		cp := *pending
		run.Hits[h.HitID] = &cp
		run.Labels[h.HitID] = label
		run.Total++

		job := captureJob{
			analysisID:      doc.AnalysisID,
			nsn:             nsn,
			hitID:           h.HitID,
			pageURL:         src,
			productImageURL: productImg,
			urlKind:         h.Links.URLKind,
			label:           label,
			timeout:         w.opts.Timeout,
			presignTTL:      w.opts.PresignTTL,
		}
		select {
		case w.jobs <- job:
			queued++
		default:
			// Queue full — mark failed.
			pending.Status = "failed"
			pending.Error = "screenshot queue full"
			cp2 := *pending
			run.Hits[h.HitID] = &cp2
			run.Done++
			run.Failed++
		}
	}

	if run.Total == 0 {
		return 0
	}
	if run.Done >= run.Total {
		run.Status = "complete"
	} else {
		run.Status = "running"
	}
	w.mu.Lock()
	// Evict old runs if map is large.
	if len(w.runs) > 200 {
		w.evictOldestLocked(50)
	}
	w.runs[doc.AnalysisID] = run
	w.mu.Unlock()
	return queued
}

// GetRun returns a copy of the run status for polling (nil if unknown).
func (w *Worker) GetRun(analysisID string) *RunStatus {
	if w == nil || analysisID == "" {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	src := w.runs[analysisID]
	if src == nil {
		return nil
	}
	return cloneRun(src)
}

func (w *Worker) loop() {
	for job := range w.jobs {
		w.processJob(job)
	}
}

func (w *Worker) processJob(job captureJob) {
	// Don't use request context — it is cancelled when the HTTP handler returns.
	pageTO := job.timeout
	if pageTO <= 0 {
		pageTO = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), pageTO+20*time.Second)
	defer cancel()

	result := &models.DataCaptureScreenshot{
		Status:    "pending",
		SourceURL: job.pageURL,
	}

	shot, err := w.capturer.CaptureEvidence(ctx, job.pageURL, job.productImageURL)
	if err != nil {
		result.Status = "failed"
		result.Error = truncateErr(err.Error(), 240)
		if isBotWalledHost(hostOf(job.pageURL)) {
			result.Kind = KindProductImage
		} else {
			result.Kind = KindPageScreenshot
		}
		w.finishJob(job, result)
		return
	}
	capturedAt := time.Now().UTC()
	key := w.store.EvidenceObjectKey(job.nsn, job.analysisID, job.hitID, capturedAt)
	meta := map[string]string{
		"nsn":         job.nsn,
		"analysis-id": job.analysisID,
		"hit-id":      job.hitID,
		"source-url":  job.pageURL,
		"url-kind":    job.urlKind,
		"content-sha": shot.SHA256,
	}
	if job.label.Merchant != "" {
		meta["merchant"] = job.label.Merchant
	}
	if job.label.Channel != "" {
		meta["channel"] = job.label.Channel
	}
	if job.label.UnitPrice > 0 {
		meta["unit-price"] = fmt.Sprintf("%.4f", job.label.UnitPrice)
	}
	if shot.Backend != "" {
		meta["capture-backend"] = shot.Backend
	}
	kind := shot.Kind
	if kind == "" {
		kind = KindPageScreenshot
	}
	meta["evidence-kind"] = kind
	ct := shot.ContentType
	if ct == "" {
		ct = "image/png"
	}
	putCtx, putCancel := context.WithTimeout(ctx, 30*time.Second)
	err = w.store.PutObject(putCtx, key, shot.PNG, ct, meta)
	putCancel()
	if err != nil {
		result.Status = "failed"
		result.Error = truncateErr(err.Error(), 240)
		result.Kind = kind
		w.finishJob(job, result)
		return
	}
	presign, _ := w.store.PresignGet(ctx, key, job.presignTTL)
	result = &models.DataCaptureScreenshot{
		Status:      "ready",
		Kind:        kind,
		Bucket:      w.store.Bucket(),
		ObjectKey:   key,
		ContentType: ct,
		CapturedAt:  capturedAt,
		SourceURL:   job.pageURL,
		Width:       shot.Width,
		Height:      shot.Height,
		SHA256:      shot.SHA256,
		URL:         presign,
	}
	w.finishJob(job, result)
}

func (w *Worker) finishJob(job captureJob, shot *models.DataCaptureScreenshot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	run := w.runs[job.analysisID]
	if run == nil {
		return
	}
	run.Hits[job.hitID] = shot
	run.Labels[job.hitID] = job.label
	run.Done++
	if shot.Status == "ready" {
		run.Ready++
	} else if shot.Status == "failed" {
		run.Failed++
	}
	if run.Done >= run.Total {
		run.Status = "complete"
	} else {
		run.Status = "running"
	}
	run.UpdatedAt = time.Now().UTC()
}

func (w *Worker) evictOldestLocked(n int) {
	// Simple: drop complete runs first.
	type item struct {
		id string
		t  time.Time
	}
	var complete []item
	for id, r := range w.runs {
		if r.Status == "complete" {
			complete = append(complete, item{id, r.UpdatedAt})
		}
	}
	// Sort by time ascending (bubble small n).
	for i := 0; i < len(complete); i++ {
		for j := i + 1; j < len(complete); j++ {
			if complete[j].t.Before(complete[i].t) {
				complete[i], complete[j] = complete[j], complete[i]
			}
		}
	}
	for i := 0; i < n && i < len(complete); i++ {
		delete(w.runs, complete[i].id)
	}
}

func cloneRun(src *RunStatus) *RunStatus {
	if src == nil {
		return nil
	}
	out := &RunStatus{
		AnalysisID: src.AnalysisID,
		NSN:        src.NSN,
		Status:     src.Status,
		Total:      src.Total,
		Done:       src.Done,
		Ready:      src.Ready,
		Failed:     src.Failed,
		UpdatedAt:  src.UpdatedAt,
		Hits:       map[string]*models.DataCaptureScreenshot{},
		Labels:     map[string]HitLabel{},
	}
	for k, v := range src.Hits {
		if v == nil {
			continue
		}
		cp := *v
		out.Hits[k] = &cp
	}
	for k, v := range src.Labels {
		out.Labels[k] = v
	}
	return out
}
