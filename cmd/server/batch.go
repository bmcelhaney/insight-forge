package main

import (
	"context"
	"sync"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/clickhouse"
	"github.com/bmcelhaney/insight-forge/internal/models"
	"github.com/google/uuid"
)

type analyzeFn func(ctx context.Context, nsn string, serpImmersive *bool, captureScreenshots bool) (models.InsightResult, models.DataCaptureDocument)

type batchItem struct {
	NSN        string `json:"nsn"`
	NSNDashed  string `json:"nsn_dashed,omitempty"`
	ProdName   string `json:"prod_name,omitempty"`
	Status     string `json:"status"` // pending | running | ok | error
	DurationMS int64  `json:"duration_ms,omitempty"`
	Analyses   int    `json:"analyses"`
	Hits       int    `json:"hits"`
	PricedHits int    `json:"priced_hits"`
	AnalysisID string `json:"analysis_id,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type batchJob struct {
	mu sync.Mutex

	ID                string
	Status            string // queued | running | complete | error
	Source            string
	Limit             int
	Total             int
	Completed         int
	Failed            int
	AnalysesWritten   int
	HitsWritten       int
	PricedHitsWritten int
	CurrentNSN        string
	CurrentIndex      int
	CurrentStartedAt  time.Time
	StartedAt         time.Time
	FinishedAt        time.Time
	AvgMS             int64
	AlreadyAnalyzed   int
	Eligible          int
	RemainingAfter    int
	Items             []batchItem
	Error             string
}

type batchStore struct {
	mu      sync.Mutex
	jobs    map[string]*batchJob
	running string
}

func newBatchStore() *batchStore {
	return &batchStore{jobs: map[string]*batchJob{}}
}

func (s *batchStore) start(pick clickhouse.PlimsPick, analyze analyzeFn, serpImmersive *bool) (*batchJob, error) {
	nsns := pick.NSNs
	s.mu.Lock()
	if s.running != "" {
		cur := s.jobs[s.running]
		s.mu.Unlock()
		if cur != nil && (cur.Status == "queued" || cur.Status == "running") {
			return nil, errBatchBusy
		}
	}
	job := &batchJob{
		ID:              uuid.NewString(),
		Status:          "queued",
		Source:          "EBS.XXSC_XXSC_PLIMS_PRODUCTS",
		Limit:           len(nsns),
		Total:           len(nsns),
		StartedAt:       time.Now().UTC(),
		AlreadyAnalyzed: pick.AlreadyAnalyzed,
		Eligible:        pick.Eligible,
		RemainingAfter:  pick.RemainingAfter,
		Items:           make([]batchItem, len(nsns)),
	}
	for i, n := range nsns {
		job.Items[i] = batchItem{
			NSN:       n.NSN,
			NSNDashed: n.NSNDashed,
			ProdName:  n.ProdName,
			Status:    "pending",
		}
	}
	s.jobs[job.ID] = job
	s.running = job.ID
	s.mu.Unlock()

	go runBatchJob(s, job, analyze, serpImmersive)
	return job, nil
}

func (s *batchStore) get(id string) *batchJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[id]
}

func (s *batchStore) clearRunning(id string) {
	s.mu.Lock()
	if s.running == id {
		s.running = ""
	}
	s.mu.Unlock()
}

var errBatchBusy = errString("a batch job is already running")

type errString string

func (e errString) Error() string { return string(e) }

func runBatchJob(s *batchStore, job *batchJob, analyze analyzeFn, serpImmersive *bool) {
	defer s.clearRunning(job.ID)
	job.mu.Lock()
	job.Status = "running"
	job.mu.Unlock()

	var totalMS int64
	okCount := 0
	for i := range job.Items {
		job.mu.Lock()
		job.Items[i].Status = "running"
		job.CurrentIndex = i + 1
		job.CurrentNSN = job.Items[i].NSN
		start := time.Now().UTC()
		job.CurrentStartedAt = start
		job.Items[i].StartedAt = start.Format(time.RFC3339)
		job.mu.Unlock()

		itemCtx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		_, doc := analyze(itemCtx, job.Items[i].NSN, serpImmersive, false)
		cancel()
		dur := time.Since(start)

		job.mu.Lock()
		job.Items[i].DurationMS = dur.Milliseconds()
		job.Items[i].FinishedAt = time.Now().UTC().Format(time.RFC3339)
		job.Items[i].AnalysisID = doc.AnalysisID
		if doc.AnalysisID == "" {
			job.Items[i].Status = "error"
			job.Items[i].Error = "analyze returned no analysis_id"
			job.Failed++
		} else {
			job.Items[i].Status = "ok"
			okCount++
			totalMS += dur.Milliseconds()
			if doc.ClickHouse != nil && doc.ClickHouse.Written {
				job.Items[i].Analyses = doc.ClickHouse.Analyses
				job.Items[i].Hits = doc.ClickHouse.Hits
				job.Items[i].PricedHits = doc.ClickHouse.PricedHits
				job.AnalysesWritten += doc.ClickHouse.Analyses
				job.HitsWritten += doc.ClickHouse.Hits
				job.PricedHitsWritten += doc.ClickHouse.PricedHits
			} else if doc.ClickHouse != nil && doc.ClickHouse.Error != "" {
				job.Items[i].Error = "analyzed, ClickHouse write failed: " + doc.ClickHouse.Error
				job.Items[i].Hits = doc.Counts.TotalHits
				job.Items[i].PricedHits = doc.Counts.PriceObservations
			} else {
				job.Items[i].Hits = doc.Counts.TotalHits
				job.Items[i].PricedHits = doc.Counts.PriceObservations
			}
		}
		job.Completed = i + 1
		if okCount > 0 {
			job.AvgMS = totalMS / int64(okCount)
		}
		job.mu.Unlock()
	}

	job.mu.Lock()
	job.Status = "complete"
	job.CurrentNSN = ""
	job.CurrentStartedAt = time.Time{}
	now := time.Now().UTC()
	job.FinishedAt = now
	job.mu.Unlock()
}

func (j *batchJob) snapshot() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	avg := j.AvgMS
	eta := int64(0)
	currentElapsed := int64(0)
	if j.Status == "running" && !j.CurrentStartedAt.IsZero() {
		currentElapsed = time.Since(j.CurrentStartedAt).Milliseconds()
	}
	remain := j.Total - j.Completed
	if remain < 0 {
		remain = 0
	}
	if j.Status == "running" && avg > 0 {
		eta = avg * int64(remain)
		if currentElapsed > 0 && remain > 0 {
			leftOnCurrent := avg - currentElapsed
			if leftOnCurrent < 0 {
				leftOnCurrent = 0
			}
			eta = leftOnCurrent + avg*int64(remain-1)
			if remain == 0 {
				eta = leftOnCurrent
			}
		}
	}
	items := make([]batchItem, len(j.Items))
	copy(items, j.Items)
	out := map[string]any{
		"job_id":              j.ID,
		"status":              j.Status,
		"source":              j.Source,
		"limit":               j.Limit,
		"total":               j.Total,
		"completed":           j.Completed,
		"failed":              j.Failed,
		"processed_ok":        j.Completed - j.Failed,
		"analyses_written":    j.AnalysesWritten,
		"hits_written":        j.HitsWritten,
		"priced_hits_written": j.PricedHitsWritten,
		"current_nsn":         j.CurrentNSN,
		"current_index":       j.CurrentIndex,
		"avg_ms":              avg,
		"eta_ms":              eta,
		"current_elapsed_ms":  currentElapsed,
		"started_at":          j.StartedAt.Format(time.RFC3339),
		"already_analyzed":    j.AlreadyAnalyzed,
		"eligible":            j.Eligible,
		"remaining_after":     j.RemainingAfter,
		"items":               items,
	}
	if !j.FinishedAt.IsZero() {
		out["finished_at"] = j.FinishedAt.Format(time.RFC3339)
		out["total_ms"] = j.FinishedAt.Sub(j.StartedAt).Milliseconds()
	}
	if j.Error != "" {
		out["error"] = j.Error
	}
	return out
}
