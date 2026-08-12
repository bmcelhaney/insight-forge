package processing

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Link verification for commercial price-evidence URLs.
//
// Philosophy: do NOT hard-block live retailers (Newegg, Sears, True Value, …).
// Those domains are fine when the listing is real. Instead:
//   1) Always reject multi-result search/hub pages (never product evidence).
//   2) Hard-block only permanently defunct hosts (Jet closed, dead aggregators).
//   3) Probe everything else that looks like a product page before trusting it.
//   4) Cache probe results so re-runs don't re-burn latency/outbound requests.
//
// Brand/SKU conflict checks remain separate (correctness, not host blocklist).

const (
	linkVerifyCacheTTLOK   = 12 * time.Hour
	linkVerifyCacheTTLFail = 2 * time.Hour
	linkVerifyTimeout      = 4 * time.Second
	// Cap probes per commercial ref so one NSN with 30 offers stays responsive.
	maxLinkProbesPerRef = 8
)

var (
	linkVerifyMu    sync.RWMutex
	linkVerifyCache = map[string]linkVerifyEntry{}

	// Shared probe budget across one enrichProductLinks run (optional via context).
	linkVerifyClient = &http.Client{
		Timeout: linkVerifyTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	// Soft-404 / delisted phrases in HTML (sites often return HTTP 200).
	soft404Phrases = []string{
		"no longer available",
		"this item is no longer",
		"product not found",
		"page not found",
		"we couldn't find",
		"we could not find",
		"isn't available",
		"is not available",
		"has been removed",
		"listing was removed",
		"item not available",
		"sorry, we can't find",
		"sorry we can't find",
		"404 error",
		"error 404",
		"sold out permanently",
		"this page does not exist",
	}
)

type linkVerifyEntry struct {
	ok     bool
	reason string
	expiry time.Time
}

type ctxKeyLinkProbeBudget struct{}

// withLinkProbeBudget attaches a shared remaining-probe counter for one analysis.
func withLinkProbeBudget(ctx context.Context, n int) context.Context {
	if n <= 0 {
		n = 40
	}
	v := int32(n)
	return context.WithValue(ctx, ctxKeyLinkProbeBudget{}, &v)
}

func takeLinkProbeSlot(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	v, ok := ctx.Value(ctxKeyLinkProbeBudget{}).(*int32)
	if !ok || v == nil {
		return true
	}
	// Simple non-atomic is OK under modest parallelism; prefer not probing when exhausted.
	if *v <= 0 {
		return false
	}
	*v--
	return true
}

// linkVerifyEnabled is true unless IF_LINK_VERIFY=0/false.
func linkVerifyEnabled() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("IF_LINK_VERIFY")))
	if raw == "0" || raw == "false" || raw == "off" || raw == "no" {
		return false
	}
	return true
}

// isPermanentlyDeadHost is the ONLY hard host blocklist for evidence URLs.
// Keep this tiny: closed businesses and non-retail aggregators — not live stores.
func isPermanentlyDeadHost(u string) bool {
	u = strings.ToLower(u)
	// Jet.com closed 2020; classic price-scrapers are not product pages.
	dead := []string{
		"jet.com",
		"pricegrabber.com",
		"nextag.com",
		"shopzilla.com",
		"become.com",
		"shopping.yahoo.com", // legacy Yahoo Shopping product URLs are largely dead
	}
	for _, h := range dead {
		if strings.Contains(u, h) {
			return true
		}
	}
	return false
}

// linkNeedsVerification is true when we should HTTP-probe before trusting the URL.
// Trusted major retailers with clear PDP paths skip the probe (latency).
func linkNeedsVerification(u string) bool {
	u = strings.TrimSpace(u)
	if u == "" || isSearchOrHubURL(u) || isPermanentlyDeadHost(u) {
		return false // not "needs verify" — already decided bad/skip
	}
	// Major retailers with known-good path shapes: accept without probe.
	if isTrustedRetailerHost(u) && looksLikeProductPath(u) {
		// Still probe Amazon /dp when we want soft-404 detection? Usually live.
		// Skip probe for speed on trusted hosts.
		return false
	}
	// Everything else with a product-looking path: verify (Newegg, regional, affiliates, HTTP, …).
	return looksLikeProductPath(u) || strings.HasPrefix(strings.ToLower(u), "http://") ||
		strings.Contains(strings.ToLower(u), "upcitemdb.com/norob")
}

func looksLikeProductPath(u string) bool {
	ul := strings.ToLower(u)
	if isSearchOrHubURL(ul) {
		return false
	}
	return strings.Contains(ul, "/dp/") || strings.Contains(ul, "/gp/product/") ||
		strings.Contains(ul, "/ip/") || strings.Contains(ul, "/p/") ||
		strings.Contains(ul, "/pd/") || strings.Contains(ul, "/product/") ||
		strings.Contains(ul, "/products/") || strings.Contains(ul, "/sku/") ||
		strings.Contains(ul, "upcitemdb.com/norob") ||
		strings.Contains(ul, "item=") // Newegg-style
}

// isEvidenceLinkCandidate is true if the URL could be product evidence after verify.
// Does not probe — structural checks only.
func isEvidenceLinkCandidate(u string) bool {
	u = strings.TrimSpace(u)
	if u == "" || isSearchOrHubURL(u) || isPermanentlyDeadHost(u) {
		return false
	}
	return looksLikeProductPath(u) || isMerchantProductURL(u)
}

// verifyProductLinkAlive probes the URL (cached). Returns true when the listing
// appears reachable and not a soft-404 / delisted page.
func verifyProductLinkAlive(ctx context.Context, rawURL string) (ok bool, reason string) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return false, "empty"
	}
	if isSearchOrHubURL(rawURL) {
		return false, "search_hub"
	}
	if isPermanentlyDeadHost(rawURL) {
		return false, "dead_host"
	}
	if !linkVerifyEnabled() {
		// Verification disabled: fall back to structural product-path check only.
		if isMerchantProductURL(rawURL) || looksLikeProductPath(rawURL) {
			return true, "verify_disabled"
		}
		return false, "verify_disabled_not_product"
	}

	cacheKey := strings.ToLower(rawURL)
	linkVerifyMu.RLock()
	if e, hit := linkVerifyCache[cacheKey]; hit && time.Now().Before(e.expiry) {
		linkVerifyMu.RUnlock()
		return e.ok, e.reason + "_cached"
	}
	linkVerifyMu.RUnlock()

	if !takeLinkProbeSlot(ctx) {
		// Budget exhausted: allow trusted PDPs, reject unknown (conservative for evidence).
		if isTrustedRetailerHost(rawURL) && looksLikeProductPath(rawURL) {
			return true, "budget_trusted"
		}
		// Prefer keeping a candidate rather than stripping everything when budget is gone
		// if it looks like a product path — prices still useful; risk is some dead links.
		if looksLikeProductPath(rawURL) {
			return true, "budget_allow"
		}
		return false, "budget_exhausted"
	}

	ok, reason = probeProductURL(ctx, rawURL)

	ttl := linkVerifyCacheTTLFail
	if ok {
		ttl = linkVerifyCacheTTLOK
	}
	linkVerifyMu.Lock()
	linkVerifyCache[cacheKey] = linkVerifyEntry{ok: ok, reason: reason, expiry: time.Now().Add(ttl)}
	// Bound cache size loosely.
	if len(linkVerifyCache) > 4000 {
		// Drop expired only.
		now := time.Now()
		for k, e := range linkVerifyCache {
			if now.After(e.expiry) {
				delete(linkVerifyCache, k)
			}
		}
	}
	linkVerifyMu.Unlock()
	return ok, reason
}

func probeProductURL(ctx context.Context, rawURL string) (bool, string) {
	// Normalize
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false, "bad_url"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false, "bad_scheme"
	}

	reqCtx, cancel := context.WithTimeout(ctx, linkVerifyTimeout)
	defer cancel()
	t0 := time.Now()
	defer addPhaseMS(ctx, phaseLinkVerify, t0)

	// Prefer HEAD (cheap). Some CDNs reject HEAD → fall back to short GET.
	ok, reason, tryGET := doProbeMethod(reqCtx, http.MethodHead, rawURL)
	if tryGET {
		ok, reason, _ = doProbeMethod(reqCtx, http.MethodGet, rawURL)
	}
	return ok, reason
}

func doProbeMethod(ctx context.Context, method, rawURL string) (ok bool, reason string, retryGET bool) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return false, "req_err", false
	}
	req.Header.Set("User-Agent", "InsightForge/1.0 (+https://github.com/bmcelhaney/insight-forge; product-link-verify)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	if method == http.MethodGet {
		// Limit body download for soft-404 sniffing.
		req.Header.Set("Range", "bytes=0-65535")
	}

	resp, err := linkVerifyClient.Do(req)
	if err != nil {
		return false, "net_err", method == http.MethodHead
	}
	defer resp.Body.Close()

	code := resp.StatusCode
	// HEAD not allowed / not implemented → GET.
	if method == http.MethodHead && (code == http.StatusMethodNotAllowed || code == http.StatusForbidden || code == http.StatusNotImplemented) {
		return false, "head_unsupported", true
	}
	// Hard not found / gone.
	if code == http.StatusNotFound || code == http.StatusGone || code == 418 {
		return false, "http_" + http.StatusText(code), false
	}
	// Auth walls / rate limit — don't treat as dead product; allow as candidate.
	if code == http.StatusUnauthorized || code == http.StatusTooManyRequests {
		return true, "http_allow_" + itoa(code), false
	}
	// Redirects to search pages are weak evidence.
	if code >= 300 && code < 400 {
		loc := resp.Header.Get("Location")
		if loc != "" && isSearchOrHubURL(loc) {
			return false, "redirect_search", false
		}
		// Other redirects: follow was limited; treat as uncertain-ok for trusted, else GET.
		if method == http.MethodHead {
			return false, "redirect", true
		}
	}
	if code >= 500 {
		// Transient server error — don't permanently kill; allow but short cache via fail TTL caller.
		return false, "http_5xx", method == http.MethodHead
	}
	if code >= 400 && code != http.StatusUnauthorized {
		return false, "http_" + itoa(code), method == http.MethodHead && code == http.StatusForbidden
	}

	// Soft-404 sniff on GET body.
	if method == http.MethodGet && resp.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		low := strings.ToLower(string(body))
		for _, p := range soft404Phrases {
			if strings.Contains(low, p) {
				// Avoid false positives on tiny nav chrome: require phrase not only in scripts.
				return false, "soft404", false
			}
		}
	}
	return true, "ok", false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// acceptEvidenceLink decides whether a URL is usable as price evidence for this identity.
// Combines structural checks, brand match, and optional live verification.
func acceptEvidenceLink(ctx context.Context, rawURL, sku, mfr, title string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || isSearchOrHubURL(rawURL) || isPermanentlyDeadHost(rawURL) {
		return false
	}
	if identityMatchScore(sku, mfr, title, rawURL) < 0 {
		return false
	}
	if !isEvidenceLinkCandidate(rawURL) && !isMerchantProductURL(rawURL) {
		return false
	}
	// Trusted PDP shape → accept without probe.
	if isTrustedRetailerHost(rawURL) && looksLikeProductPath(rawURL) && !linkNeedsVerification(rawURL) {
		return true
	}
	if linkNeedsVerification(rawURL) || !isTrustedRetailerHost(rawURL) {
		ok, _ := verifyProductLinkAlive(ctx, rawURL)
		return ok
	}
	return isMerchantProductURL(rawURL) || looksLikeProductPath(rawURL)
}

// clearLinkVerifyCache is for tests.
func clearLinkVerifyCache() {
	linkVerifyMu.Lock()
	linkVerifyCache = map[string]linkVerifyEntry{}
	linkVerifyMu.Unlock()
}
