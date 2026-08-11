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
	AnalysisID string                                   `json:"analysis_id"`
	NSN        string                                   `json:"nsn,omitempty"`
	Status     string                                   `json:"status"` // pending | running | complete
	Total      int                                      `json:"total"`
	Done       int                                      `json:"done"`
	Ready      int                                      `json:"ready"`
	Failed     int                                      `json:"failed"`
	Skipped    int                                      `json:"skipped"`
	Hits       map[string]*models.DataCaptureScreenshot `json:"hits"` // hit_id → screenshot
	Labels     map[string]HitLabel                      `json:"labels,omitempty"`
	// DataCapture is the full data-capture document for this run with
	// hits[].proof.screenshot updated as each capture finishes.
	// Ready shots include durable Tigris bucket + object_key.
	// Skipped/failed still retain source_url (product page).
	DataCapture *models.DataCaptureDocument `json:"data_capture,omitempty"`
	UpdatedAt   time.Time                   `json:"updated_at"`
	// Deadline is when remaining pending captures are abandoned (not exported).
	deadline time.Time
}

type captureJob struct {
	analysisID string
	nsn        string
	hitID      string
	pageURL    string
	urlKind    string
	label      HitLabel
	timeout    time.Duration
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
		opts.MaxPerRun = 6
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.BatchTimeout <= 0 {
		// Whole batch budget — after this, remaining jobs are skipped; product URLs kept.
		opts.BatchTimeout = 45 * time.Second
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
		if n > 10 {
			n = 10
		}
		for i := 0; i < n; i++ {
			go w.loop()
		}
	})

	nsn := doc.Query.NSN
	if nsn == "" {
		nsn = doc.Query.EntityID
	}

	now := time.Now().UTC()
	run := &RunStatus{
		AnalysisID: doc.AnalysisID,
		NSN:        nsn,
		Status:     "pending",
		Hits:       map[string]*models.DataCaptureScreenshot{},
		Labels:     map[string]HitLabel{},
		UpdatedAt:  now,
		deadline:   now.Add(w.opts.BatchTimeout),
	}

	queued := 0
	for i := range doc.Hits {
		h := &doc.Hits[i]
		if !eligibleForScreenshot(h) {
			continue
		}
		src := strings.TrimSpace(h.Links.URL)
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
		if h.Proof == nil {
			h.Proof = &models.DataCaptureProof{}
		}

		// Bot-walled hosts: skip page capture immediately; keep product URL.
		if isBotWalledHost(hostOf(src)) {
			skipped := &models.DataCaptureScreenshot{
				Status:    "skipped",
				Kind:      KindPageScreenshot,
				SourceURL: src,
				Error:     "bot-protected merchant; product URL retained without page capture",
			}
			h.Proof.Screenshot = skipped
			cp := *skipped
			run.Hits[h.HitID] = &cp
			run.Labels[h.HitID] = label
			run.Total++
			run.Done++
			run.Skipped++
			continue
		}

		// Cap live page-capture attempts (bot-walled skips don't consume the cap).
		if queued >= w.opts.MaxPerRun {
			skipped := &models.DataCaptureScreenshot{
				Status:    "skipped",
				Kind:      KindPageScreenshot,
				SourceURL: src,
				Error:     "screenshot cap reached; product URL retained without page capture",
			}
			h.Proof.Screenshot = skipped
			cp := *skipped
			run.Hits[h.HitID] = &cp
			run.Labels[h.HitID] = label
			run.Total++
			run.Done++
			run.Skipped++
			continue
		}

		pending := &models.DataCaptureScreenshot{
			Status:    "pending",
			Kind:      KindPageScreenshot,
			SourceURL: src,
		}
		h.Proof.Screenshot = pending
		cp := *pending
		run.Hits[h.HitID] = &cp
		run.Labels[h.HitID] = label
		run.Total++

		job := captureJob{
			analysisID: doc.AnalysisID,
			nsn:        nsn,
			hitID:      h.HitID,
			pageURL:    src,
			urlKind:    h.Links.URLKind,
			label:      label,
			timeout:    w.opts.Timeout,
		}
		select {
		case w.jobs <- job:
			queued++
		default:
			pending.Status = "skipped"
			pending.Error = "screenshot queue full; product URL retained"
			cp2 := *pending
			run.Hits[h.HitID] = &cp2
			h.Proof.Screenshot = &cp2
			run.Done++
			run.Skipped++
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
	run.DataCapture = cloneDataCaptureDocument(doc)
	w.mu.Lock()
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
	// If the batch budget is exhausted, skip without another network call.
	// Product URL stays on the proof object for Windmill/DB storage.
	if w.batchExpired(job.analysisID) {
		w.finishJob(job, &models.DataCaptureScreenshot{
			Status:    "skipped",
			Kind:      KindPageScreenshot,
			SourceURL: job.pageURL,
			Error:     "batch time budget exceeded; product URL retained without page capture",
		})
		return
	}

	pageTO := job.timeout
	if pageTO <= 0 {
		pageTO = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), pageTO+5*time.Second)
	defer cancel()

	result := &models.DataCaptureScreenshot{
		Status:    "pending",
		Kind:      KindPageScreenshot,
		SourceURL: job.pageURL,
	}

	shot, err := w.capturer.CapturePNG(ctx, job.pageURL)
	if err != nil {
		result.Status = "failed"
		result.Error = truncateErr(err.Error(), 240)
		w.finishJob(job, result)
		return
	}
	// Re-check budget after slow capture so we don't spend more time on Tigris if already late.
	if w.batchExpired(job.analysisID) {
		// Still upload if we already have pixels — capture work is done.
	}
	capturedAt := time.Now().UTC()
	key := w.store.EvidenceObjectKey(job.nsn, job.analysisID, job.hitID, capturedAt)
	meta := map[string]string{
		"nsn":           job.nsn,
		"analysis-id":   job.analysisID,
		"hit-id":        job.hitID,
		"source-url":    job.pageURL,
		"url-kind":      job.urlKind,
		"content-sha":   shot.SHA256,
		"evidence-kind": KindPageScreenshot,
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
	ct := shot.ContentType
	if ct == "" {
		ct = "image/png"
	}
	putCtx, putCancel := context.WithTimeout(ctx, 15*time.Second)
	err = w.store.PutObject(putCtx, key, shot.PNG, ct, meta)
	putCancel()
	if err != nil {
		result.Status = "failed"
		result.Error = truncateErr(err.Error(), 240)
		w.finishJob(job, result)
		return
	}
	result = &models.DataCaptureScreenshot{
		Status:      "ready",
		Kind:        KindPageScreenshot,
		Bucket:      w.store.Bucket(),
		ObjectKey:   key,
		ContentType: ct,
		CapturedAt:  capturedAt,
		SourceURL:   job.pageURL,
		Width:       shot.Width,
		Height:      shot.Height,
		SHA256:      shot.SHA256,
	}
	w.finishJob(job, result)
}

func (w *Worker) batchExpired(analysisID string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	run := w.runs[analysisID]
	if run == nil || run.deadline.IsZero() {
		return false
	}
	return time.Now().After(run.deadline)
}

func (w *Worker) finishJob(job captureJob, shot *models.DataCaptureScreenshot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	run := w.runs[job.analysisID]
	if run == nil {
		return
	}
	// Only count once: upgrade from pending (or first write).
	if prev := run.Hits[job.hitID]; prev != nil && prev.Status != "pending" {
		return
	}
	cp := *shot
	run.Hits[job.hitID] = &cp
	run.Labels[job.hitID] = job.label
	if run.DataCapture != nil {
		applyScreenshotToDocument(run.DataCapture, job.hitID, &cp)
	}
	run.Done++
	switch shot.Status {
	case "ready":
		run.Ready++
	case "failed":
		run.Failed++
	case "skipped":
		run.Skipped++
	}
	if run.Done >= run.Total {
		run.Status = "complete"
	} else {
		run.Status = "running"
	}
	run.UpdatedAt = time.Now().UTC()
}

func applyScreenshotToDocument(doc *models.DataCaptureDocument, hitID string, shot *models.DataCaptureScreenshot) {
	if doc == nil || shot == nil || hitID == "" {
		return
	}
	for i := range doc.Hits {
		if doc.Hits[i].HitID != hitID {
			continue
		}
		if doc.Hits[i].Proof == nil {
			doc.Hits[i].Proof = &models.DataCaptureProof{}
		}
		cp := *shot
		doc.Hits[i].Proof.Screenshot = &cp
		return
	}
}

func cloneDataCaptureDocument(src *models.DataCaptureDocument) *models.DataCaptureDocument {
	if src == nil {
		return nil
	}
	// Shallow-copy struct then deep-copy hits slice + nested proof pointers we mutate.
	out := *src
	if src.Hits != nil {
		out.Hits = make([]models.DataCaptureHit, len(src.Hits))
		copy(out.Hits, src.Hits)
		for i := range out.Hits {
			if src.Hits[i].Proof != nil {
				p := *src.Hits[i].Proof
				if src.Hits[i].Proof.Screenshot != nil {
					s := *src.Hits[i].Proof.Screenshot
					p.Screenshot = &s
				}
				out.Hits[i].Proof = &p
			}
			if src.Hits[i].Pricing != nil {
				pr := *src.Hits[i].Pricing
				out.Hits[i].Pricing = &pr
			}
			if src.Hits[i].Links != nil {
				ln := *src.Hits[i].Links
				out.Hits[i].Links = &ln
			}
			if src.Hits[i].Attributes != nil {
				out.Hits[i].Attributes = map[string]any{}
				for k, v := range src.Hits[i].Attributes {
					out.Hits[i].Attributes[k] = v
				}
			}
		}
	}
	if src.Sources != nil {
		out.Sources = append([]models.DataCaptureSource(nil), src.Sources...)
	}
	if src.Scores != nil {
		sc := *src.Scores
		out.Scores = &sc
	}
	return &out
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
		AnalysisID:  src.AnalysisID,
		NSN:         src.NSN,
		Status:      src.Status,
		Total:       src.Total,
		Done:        src.Done,
		Ready:       src.Ready,
		Failed:      src.Failed,
		Skipped:     src.Skipped,
		UpdatedAt:   src.UpdatedAt,
		Hits:        map[string]*models.DataCaptureScreenshot{},
		Labels:      map[string]HitLabel{},
		DataCapture: cloneDataCaptureDocument(src.DataCapture),
		deadline:    src.deadline,
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
