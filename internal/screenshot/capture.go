package screenshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Evidence kinds stored on proof.screenshot.kind.
const (
	KindPageScreenshot = "page_screenshot" // full rendered page
	KindProductImage   = "product_image"   // catalog photo (bot-walled merchants)
)

// Result is a captured visual evidence image.
type Result struct {
	PNG         []byte
	ContentType string
	Width       int
	Height      int
	SHA256      string
	HTTPStatus  int
	Backend     string
	Kind        string // page_screenshot | product_image
}

// Backend identifiers.
const (
	BackendThum      = "thum"      // image.thum.io HTTP screenshots (default)
	BackendMicrolink = "microlink" // api.microlink.io
	BackendChrome    = "chrome"    // local chromedp (legacy; often fails on retail PDPs)
)

// Options controls capture.
type Options struct {
	// Backend: thum | microlink | chrome (default thum).
	Backend string
	// Timeout per capture (default 25s for HTTP backends).
	Timeout time.Duration
	Width   int
	Height  int
	// ChromePath only used when Backend=chrome.
	ChromePath string
	// ThumAuth optional thum.io auth token (IF_THUM_AUTH).
	ThumAuth string
	// MicrolinkKey optional API key (IF_MICROLINK_KEY).
	MicrolinkKey string
	// HTTPClient optional override.
	HTTPClient *http.Client
}

// Capturer fetches page screenshots via a pluggable backend.
// Default is thum.io — no local Chrome. Local Chrome on sprites repeatedly
// hard-timeouts on retail PDPs (bot walls / never-fire load events).
type Capturer struct {
	opts   Options
	client *http.Client
}

// NewCapturer builds a capturer.
func NewCapturer(opts Options) *Capturer {
	opts.Backend = strings.ToLower(strings.TrimSpace(opts.Backend))
	if opts.Backend == "" {
		opts.Backend = strings.ToLower(strings.TrimSpace(os.Getenv("IF_SCREENSHOT_BACKEND")))
	}
	if opts.Backend == "" {
		opts.Backend = BackendThum
	}
	if opts.Timeout <= 0 {
		if opts.Backend == BackendChrome {
			opts.Timeout = 18 * time.Second
		} else {
			opts.Timeout = 30 * time.Second
		}
	}
	if opts.Width <= 0 {
		opts.Width = 1280
	}
	if opts.Height <= 0 {
		opts.Height = 720
	}
	if opts.ThumAuth == "" {
		opts.ThumAuth = strings.TrimSpace(os.Getenv("IF_THUM_AUTH"))
	}
	if opts.MicrolinkKey == "" {
		opts.MicrolinkKey = strings.TrimSpace(os.Getenv("IF_MICROLINK_KEY"))
	}
	if opts.ChromePath == "" && opts.Backend == BackendChrome {
		opts.ChromePath = findChrome()
	}
	cli := opts.HTTPClient
	if cli == nil {
		cli = &http.Client{
			Timeout: opts.Timeout + 10*time.Second,
			// Follow redirects; thum/microlink may redirect to CDN.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 8 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		}
	}
	return &Capturer{opts: opts, client: cli}
}

// Backend returns the active backend name.
func (c *Capturer) Backend() string {
	if c == nil {
		return ""
	}
	return c.opts.Backend
}

// Parallelism is how many captures may run concurrently for this backend.
func (c *Capturer) Parallelism() int {
	if c == nil {
		return 1
	}
	switch c.opts.Backend {
	case BackendChrome:
		return 1 // Chrome on a sprite cannot safely parallelize
	default:
		return 8 // HTTP image fetch / thum — keep queue short for Windmill
	}
}

// ChromePath returns chrome path when backend is chrome.
func (c *Capturer) ChromePath() string {
	if c == nil {
		return ""
	}
	return c.opts.ChromePath
}

// Available is true when the configured backend can run.
func (c *Capturer) Available() bool {
	if c == nil {
		return false
	}
	switch c.opts.Backend {
	case BackendChrome:
		return c.opts.ChromePath != ""
	case BackendThum, BackendMicrolink:
		return true
	default:
		return false
	}
}

// CapturePNG is a convenience wrapper (page capture only, no product-image fallback).
func (c *Capturer) CapturePNG(ctx context.Context, pageURL string) (*Result, error) {
	return c.CaptureEvidence(ctx, pageURL, "")
}

// CaptureEvidence returns visual proof for a price hit.
//
// Strategy (why this exists):
//   Amazon / Walmart / Home Depot / etc. serve CAPTCHA / "robot or human" /
//   "continue shopping" interstitial pages to datacenter screenshotters
//   (thum, microlink, headless Chrome). Those images are useless as price
//   evidence. For bot-walled hosts we instead archive a product catalog photo
//   (SerpAPI Google Shopping thumbnail). Friendly hosts still get a full-page
//   screenshot of the PDP.
func (c *Capturer) CaptureEvidence(ctx context.Context, pageURL, productImageURL string) (*Result, error) {
	if c == nil {
		return nil, fmt.Errorf("screenshot: capturer nil")
	}
	if err := validatePublicHTTPURL(pageURL); err != nil {
		return nil, err
	}
	if !c.Available() {
		return nil, fmt.Errorf("screenshot: backend %q unavailable", c.opts.Backend)
	}

	productImageURL = strings.TrimSpace(productImageURL)
	host := hostOf(pageURL)

	// Fast path (preferred for Windmill latency): SerpAPI / catalog product photo.
	// Typically 200–800ms vs 2–5s for a full page render — and works on bot-walled hosts.
	if productImageURL != "" && validatePublicHTTPURL(productImageURL) == nil {
		if res, err := c.downloadAsResult(ctx, productImageURL, "product_image"); err == nil {
			res.Kind = KindProductImage
			res.Backend = "product_image"
			return res, nil
		}
		// Fall through to page capture on friendly hosts only.
	}

	// Bot-walled big-box: never full-page render (CAPTCHA / interstitial only).
	if isBotWalledHost(host) {
		if productImageURL != "" {
			return nil, fmt.Errorf("screenshot: bot-protected host %s — product image fetch failed", host)
		}
		return nil, fmt.Errorf("screenshot: bot-protected host %s — page capture skipped; no product image on hit", host)
	}

	// Friendly hosts without a product photo: full page screenshot (slower).
	var res *Result
	var err error
	switch c.opts.Backend {
	case BackendChrome:
		res, err = c.captureChrome(ctx, pageURL)
	case BackendMicrolink:
		res, err = c.captureMicrolink(ctx, pageURL)
	default:
		res, err = c.captureThum(ctx, pageURL)
	}
	if err != nil {
		return nil, err
	}
	if res != nil && res.Kind == "" {
		res.Kind = KindPageScreenshot
	}
	return res, nil
}

func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// isBotWalledHost is true for merchants that reliably block automated page
// screenshots with CAPTCHA / interstitial walls from datacenter IPs.
func isBotWalledHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	// Strip leading www.
	host = strings.TrimPrefix(host, "www.")
	blocked := []string{
		"amazon.com", "amazon.co.uk", "amazon.ca", "amazon.de", "amazon.fr",
		"walmart.com", "walmart.ca",
		"homedepot.com", "homedepot.ca",
		"lowes.com",
		"target.com",
		"bestbuy.com",
		"costco.com",
		"ebay.com", "ebay.co.uk",
		"newegg.com", "neweggbusiness.com",
		"wayfair.com",
		"samsung.com", "apple.com",
		"instagram.com", "facebook.com",
	}
	for _, b := range blocked {
		if host == b || strings.HasSuffix(host, "."+b) {
			return true
		}
	}
	// Common Amazon regional / CDN hosts used as storefronts.
	if strings.Contains(host, "amazon.") || strings.Contains(host, "amzn.") {
		return true
	}
	return false
}

func (c *Capturer) downloadAsResult(ctx context.Context, imgURL, backend string) (*Result, error) {
	img, ct, err := c.downloadImage(ctx, imgURL)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(img)
	return &Result{
		PNG:         img,
		ContentType: ct,
		Width:       c.opts.Width,
		Height:      c.opts.Height,
		SHA256:      hex.EncodeToString(sum[:]),
		Backend:     backend,
	}, nil
}

// captureThum uses image.thum.io — no browser process on the sprite.
// Docs: https://www.thum.io/documentation
func (c *Capturer) captureThum(ctx context.Context, pageURL string) (*Result, error) {
	// Prefer cropped viewport for evidence cards.
	// Path segments before the target URL control render options.
	// auth/{token}/ is optional for higher limits.
	var b strings.Builder
	b.WriteString("https://image.thum.io/get/")
	if c.opts.ThumAuth != "" {
		b.WriteString("auth/")
		b.WriteString(c.opts.ThumAuth)
		b.WriteByte('/')
	}
	// wait/1 keeps latency down; product photos are preferred when available.
	b.WriteString(fmt.Sprintf("width/%d/crop/%d/noanimate/wait/1/", c.opts.Width, c.opts.Height))
	// Target URL is appended raw (thum accepts full https://... including query).
	b.WriteString(pageURL)

	reqURL := b.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("screenshot thum: build request: %w", err)
	}
	req.Header.Set("User-Agent", "InsightForge/1.0 (+pricing-evidence-capture)")
	req.Header.Set("Accept", "image/png,image/*;q=0.8,*/*;q=0.5")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("screenshot thum: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20)) // 12 MiB cap
	if err != nil {
		return nil, fmt.Errorf("screenshot thum: read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("screenshot thum: http %d (%s)", resp.StatusCode, truncateBytes(body, 120))
	}
	if len(body) < 100 || !looksLikeImage(body) {
		return nil, fmt.Errorf("screenshot thum: non-image response (%d bytes)", len(body))
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/png"
	}
	sum := sha256.Sum256(body)
	return &Result{
		PNG:         body,
		ContentType: ct,
		Width:       c.opts.Width,
		Height:      c.opts.Height,
		SHA256:      hex.EncodeToString(sum[:]),
		HTTPStatus:  resp.StatusCode,
		Backend:     BackendThum,
		Kind:        KindPageScreenshot,
	}, nil
}

// captureMicrolink uses api.microlink.io screenshot endpoint.
func (c *Capturer) captureMicrolink(ctx context.Context, pageURL string) (*Result, error) {
	q := url.Values{}
	q.Set("url", pageURL)
	q.Set("screenshot", "true")
	q.Set("meta", "false")
	q.Set("embed", "screenshot.url")
	apiURL := "https://api.microlink.io/?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("screenshot microlink: build request: %w", err)
	}
	req.Header.Set("User-Agent", "InsightForge/1.0 (+pricing-evidence-capture)")
	if c.opts.MicrolinkKey != "" {
		req.Header.Set("x-api-key", c.opts.MicrolinkKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("screenshot microlink: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("screenshot microlink: read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("screenshot microlink: http %d (%s)", resp.StatusCode, truncateBytes(raw, 120))
	}
	// Parse JSON for data.screenshot.url
	shotURL, width, height, err := parseMicrolinkScreenshot(raw)
	if err != nil {
		return nil, err
	}
	img, ct, err := c.downloadImage(ctx, shotURL)
	if err != nil {
		return nil, fmt.Errorf("screenshot microlink download: %w", err)
	}
	if width <= 0 {
		width = c.opts.Width
	}
	if height <= 0 {
		height = c.opts.Height
	}
	sum := sha256.Sum256(img)
	return &Result{
		PNG:         img,
		ContentType: ct,
		Width:       width,
		Height:      height,
		SHA256:      hex.EncodeToString(sum[:]),
		Backend:     BackendMicrolink,
	}, nil
}

func (c *Capturer) downloadImage(ctx context.Context, imgURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("http %d", resp.StatusCode)
	}
	if !looksLikeImage(body) {
		return nil, "", fmt.Errorf("not an image")
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/png"
	}
	return body, ct, nil
}

func parseMicrolinkScreenshot(raw []byte) (shotURL string, width, height int, err error) {
	// Minimal parse without heavy deps — look for screenshot url field structure.
	// Expected: {"status":"success","data":{"screenshot":{"url":"...","width":N,"height":N}}}
	s := string(raw)
	if !strings.Contains(s, `"success"`) && !strings.Contains(s, `"status":"success"`) {
		// still try to extract url
	}
	// crude extraction
	if i := strings.Index(s, `"screenshot"`); i >= 0 {
		rest := s[i:]
		if u := jsonStringField(rest, "url"); u != "" {
			shotURL = u
		}
		width = jsonIntField(rest, "width")
		height = jsonIntField(rest, "height")
	}
	if shotURL == "" {
		// top-level data.screenshot.url alternate
		shotURL = jsonStringField(s, "url")
	}
	if shotURL == "" || !strings.HasPrefix(shotURL, "http") {
		return "", 0, 0, fmt.Errorf("screenshot microlink: no screenshot url in response")
	}
	return shotURL, width, height, nil
}

func jsonStringField(s, key string) string {
	// Find "key":"value"
	pat := `"` + key + `":"`
	i := strings.Index(s, pat)
	if i < 0 {
		return ""
	}
	rest := s[i+len(pat):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	// unescape simple \/
	return strings.ReplaceAll(rest[:j], `\/`, `/`)
}

func jsonIntField(s, key string) int {
	pat := `"` + key + `":`
	i := strings.Index(s, pat)
	if i < 0 {
		return 0
	}
	rest := strings.TrimSpace(s[i+len(pat):])
	n := 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
		if n > 100000 {
			break
		}
	}
	return n
}

func looksLikeImage(b []byte) bool {
	if len(b) < 8 {
		return false
	}
	// PNG
	if b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G' {
		return true
	}
	// JPEG
	if b[0] == 0xFF && b[1] == 0xD8 {
		return true
	}
	// GIF
	if string(b[:3]) == "GIF" {
		return true
	}
	// WEBP (RIFF....WEBP)
	if len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return true
	}
	return false
}

func truncateBytes(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// --- SSRF guard (shared) ---

func validatePublicHTTPURL(raw string) error {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("screenshot: invalid url")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("screenshot: only http/https allowed")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("screenshot: missing host")
	}
	low := strings.ToLower(host)
	if low == "localhost" || strings.HasSuffix(low, ".local") || low == "metadata.google.internal" {
		return fmt.Errorf("screenshot: blocked host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("screenshot: blocked private ip")
		}
	}
	ips, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err == nil {
		for _, ipa := range ips {
			ip := ipa.IP
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				return fmt.Errorf("screenshot: blocked private resolved ip")
			}
		}
	}
	return nil
}

// findChrome is only used when Backend=chrome (legacy).
func findChrome() string {
	if p := strings.TrimSpace(os.Getenv("IF_CHROME_PATH")); p != "" {
		if isRunnableChrome(p) {
			return p
		}
	}
	home := strings.TrimSpace(os.Getenv("HOME"))
	candidates := []string{
		home + "/chrome-for-testing/chrome-headless-shell-linux64/chrome-headless-shell",
		"/home/sprite/chrome-for-testing/chrome-headless-shell-linux64/chrome-headless-shell",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/snap/bin/chromium",
		"/usr/bin/chromium",
	}
	for _, p := range candidates {
		if isRunnableChrome(p) {
			return p
		}
	}
	return ""
}

func isRunnableChrome(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	head := string(buf[:n])
	if strings.HasPrefix(head, "#!") {
		if strings.Contains(head, "requires the chromium snap") ||
			strings.Contains(head, "snap install chromium") {
			return false
		}
	}
	return true
}
