package processing

import (
	"context"
	"sync/atomic"
	"time"
)

// AnalyzePhaseClock accumulates network milliseconds for one analyze/synthesize.
// Safe for concurrent probes (Serp, Immersive, UPCItemDB, link-verify).
type AnalyzePhaseClock struct {
	SerpMS       atomic.Int64
	ImmersiveMS  atomic.Int64
	UPCMS        atomic.Int64
	LinkVerifyMS atomic.Int64
}

type ctxKeyPhaseClock struct{}

const (
	phaseSerp       = "serp"
	phaseImmersive  = "immersive"
	phaseUPC        = "upc"
	phaseLinkVerify = "link_verify"
)

// WithPhaseClock attaches a fresh clock to ctx. Call once per analyze.
func WithPhaseClock(ctx context.Context) (context.Context, *AnalyzePhaseClock) {
	if ctx == nil {
		ctx = context.Background()
	}
	if existing := phaseClock(ctx); existing != nil {
		return ctx, existing
	}
	c := &AnalyzePhaseClock{}
	return context.WithValue(ctx, ctxKeyPhaseClock{}, c), c
}

func phaseClock(ctx context.Context) *AnalyzePhaseClock {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(ctxKeyPhaseClock{}).(*AnalyzePhaseClock)
	return c
}

func addPhaseMS(ctx context.Context, which string, started time.Time) {
	ms := time.Since(started).Milliseconds()
	if ms < 0 {
		return
	}
	c := phaseClock(ctx)
	if c == nil {
		return
	}
	switch which {
	case phaseSerp:
		c.SerpMS.Add(ms)
	case phaseImmersive:
		c.ImmersiveMS.Add(ms)
	case phaseUPC:
		c.UPCMS.Add(ms)
	case phaseLinkVerify:
		c.LinkVerifyMS.Add(ms)
	}
}

func phaseTimingsMap(clock *AnalyzePhaseClock, extra map[string]int64) map[string]int64 {
	out := map[string]int64{}
	for k, v := range extra {
		out[k] = v
	}
	if clock == nil {
		return out
	}
	out["serp_ms"] = clock.SerpMS.Load()
	out["immersive_ms"] = clock.ImmersiveMS.Load()
	out["upc_ms"] = clock.UPCMS.Load()
	out["link_verify_ms"] = clock.LinkVerifyMS.Load()
	return out
}
