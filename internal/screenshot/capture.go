package screenshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Result is a captured page image.
type Result struct {
	PNG         []byte // we capture PNG then callers may re-encode
	ContentType string
	Width       int
	Height      int
	SHA256      string
	HTTPStatus  int // best-effort; 0 if unknown
}

// Options controls a capture session.
type Options struct {
	// Timeout per page (hard wall-clock; default 25s).
	Timeout time.Duration
	// Viewport width/height (default 1280x720).
	Width  int
	Height int
	// ChromePath overrides binary discovery (optional).
	ChromePath string
	// BrowserStartTimeout is how long to wait for Chrome DevTools websocket (default 45s).
	BrowserStartTimeout time.Duration
	// Settle is how long to wait after navigation starts before screenshot (default 2.5s).
	Settle time.Duration
}

// Capturer takes screenshots of public HTTPS pages (SSRF-safe).
//
// Design notes (sprite / retail sites):
//   - One shared Chrome process for speed.
//   - Navigate does NOT wait for full page load (Walmart/HomeDepot SPAs hang forever
//     on chromedp.Navigate's load-event wait → worker stuck "running" for 10+ min).
//   - Every capture has a hard wall-clock deadline that force-kills Chrome if chromedp
//     ignores context cancel, so the async worker can never block the queue indefinitely.
type Capturer struct {
	opts Options

	mu            sync.Mutex
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
	captureN      int // successful or attempted captures on current browser
}

// NewCapturer builds a capturer. Chrome/Chromium must be installed on the host.
func NewCapturer(opts Options) *Capturer {
	if opts.Timeout <= 0 {
		opts.Timeout = 25 * time.Second
	}
	if opts.Width <= 0 {
		opts.Width = 1280
	}
	if opts.Height <= 0 {
		opts.Height = 720
	}
	if opts.BrowserStartTimeout <= 0 {
		opts.BrowserStartTimeout = 45 * time.Second
	}
	if opts.Settle <= 0 {
		opts.Settle = 2500 * time.Millisecond
	}
	if opts.ChromePath == "" {
		opts.ChromePath = findChrome()
	}
	return &Capturer{opts: opts}
}

// ChromePath returns the resolved browser binary (empty if unavailable).
func (c *Capturer) ChromePath() string {
	if c == nil {
		return ""
	}
	return c.opts.ChromePath
}

// Available reports whether a Chrome binary was found.
func (c *Capturer) Available() bool {
	return c != nil && c.opts.ChromePath != ""
}

// Close shuts down the shared browser (optional; process exit also cleans up).
func (c *Capturer) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shutdownLocked()
}

type captureOutcome struct {
	res *Result
	err error
}

// CapturePNG loads pageURL and returns a viewport PNG (not full-page).
func (c *Capturer) CapturePNG(ctx context.Context, pageURL string) (*Result, error) {
	if c == nil {
		return nil, fmt.Errorf("screenshot: capturer nil")
	}
	if err := validatePublicHTTPURL(pageURL); err != nil {
		return nil, err
	}
	if c.opts.ChromePath == "" {
		return nil, fmt.Errorf("screenshot: chrome/chromium not found on host")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Hard wall-clock: never block the worker longer than Timeout+grace,
	// even if chromedp/Chrome ignore context cancellation.
	hardLimit := c.opts.Timeout + 8*time.Second
	if dl, ok := ctx.Deadline(); ok {
		if remain := time.Until(dl); remain > 0 && remain < hardLimit {
			hardLimit = remain
		}
	}

	outCh := make(chan captureOutcome, 1)
	go func() {
		res, err := c.captureWithRetry(ctx, pageURL)
		outCh <- captureOutcome{res, err}
	}()

	timer := time.NewTimer(hardLimit)
	defer timer.Stop()

	select {
	case out := <-outCh:
		return out.res, out.err
	case <-timer.C:
		// Nuclear: kill Chrome so the inner goroutine can unblock.
		c.mu.Lock()
		c.shutdownLocked()
		c.mu.Unlock()
		// Don't wait forever for the goroutine — return immediately so the worker advances.
		return nil, fmt.Errorf("screenshot capture: hard timeout after %s (browser recycled)", hardLimit)
	case <-ctx.Done():
		c.mu.Lock()
		c.shutdownLocked()
		c.mu.Unlock()
		return nil, fmt.Errorf("screenshot capture: %w", ctx.Err())
	}
}

func (c *Capturer) captureWithRetry(ctx context.Context, pageURL string) (*Result, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			c.mu.Lock()
			c.shutdownLocked()
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
		res, err := c.captureOnce(ctx, pageURL)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !isBrowserStartError(err) && !isHungBrowserError(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func isBrowserStartError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "websocket url timeout") ||
		strings.Contains(s, "chrome failed to start") ||
		strings.Contains(s, "browser start") ||
		strings.Contains(s, "invalid context")
}

func isHungBrowserError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "hard timeout") ||
		strings.Contains(s, "target closed") ||
		strings.Contains(s, "browser has been closed") ||
		strings.Contains(s, "-32000") // CDP target closed
}

func (c *Capturer) captureOnce(ctx context.Context, pageURL string) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	browserCtx, err := c.ensureBrowser()
	if err != nil {
		return nil, fmt.Errorf("screenshot capture: %w", err)
	}

	// Fresh tab per URL. Do not wrap browserCtx in WithTimeout+cancel —
	// chromedp treats cancel of the browser-root context as browser death.
	tabCtx, tabCancel := chromedp.NewContext(browserCtx)
	defer tabCancel()

	pageTO := c.opts.Timeout
	tabCtx, timeoutCancel := context.WithTimeout(tabCtx, pageTO)
	defer timeoutCancel()

	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			tabCancel()
		case <-stopWatch:
		}
	}()

	var buf []byte
	settle := c.opts.Settle
	err = chromedp.Run(tabCtx,
		chromedp.EmulateViewport(int64(c.opts.Width), int64(c.opts.Height)),
		// Issue navigation without waiting for the full load event. Retail SPAs
		// and bot walls often never fire load, which hung the old chromedp.Navigate
		// path and left the worker "running" for 10+ minutes.
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, _, _, _, e := page.Navigate(pageURL).Do(ctx)
			return e
		}),
		chromedp.Sleep(settle),
		chromedp.CaptureScreenshot(&buf),
	)
	// Always close the tab promptly (defer tabCancel also runs).
	tabCancel()

	if err != nil {
		if browserCtx.Err() != nil {
			c.mu.Lock()
			if c.browserCtx == browserCtx {
				c.shutdownLocked()
			}
			c.mu.Unlock()
		}
		return nil, fmt.Errorf("screenshot capture: %w", err)
	}
	if len(buf) < 100 {
		return nil, fmt.Errorf("screenshot: empty image")
	}

	c.mu.Lock()
	c.captureN++
	// Recycle periodically so zombie renderers don't accumulate.
	if c.captureN >= 12 {
		c.shutdownLocked()
	}
	c.mu.Unlock()

	sum := sha256.Sum256(buf)
	return &Result{
		PNG:         buf,
		ContentType: "image/png",
		Width:       c.opts.Width,
		Height:      c.opts.Height,
		SHA256:      hex.EncodeToString(sum[:]),
	}, nil
}

// ensureBrowser starts (or reuses) a single Chrome process.
func (c *Capturer) ensureBrowser() (context.Context, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.browserCtx != nil && c.browserCtx.Err() == nil {
		return c.browserCtx, nil
	}
	c.shutdownLocked()

	startTO := c.opts.BrowserStartTimeout
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(c.opts.ChromePath),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("disable-background-networking", true),
		// Block heavy extras that slow or hang headless captures.
		chromedp.Flag("blink-settings", "imagesEnabled=true"),
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 InsightForge/1.0"),
		chromedp.WindowSize(c.opts.Width, c.opts.Height),
		chromedp.WSURLReadTimeout(startTO),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	// Run on browserCtx itself (never a cancelable child — that kills the browser).
	errCh := make(chan error, 1)
	go func() {
		errCh <- chromedp.Run(browserCtx)
	}()
	select {
	case err := <-errCh:
		if err != nil {
			browserCancel()
			allocCancel()
			return nil, fmt.Errorf("browser start (%s): %w", c.opts.ChromePath, err)
		}
	case <-time.After(startTO):
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("browser start (%s): timed out after %s", c.opts.ChromePath, startTO)
	}

	c.allocCancel = allocCancel
	c.browserCtx = browserCtx
	c.browserCancel = browserCancel
	c.captureN = 0
	return browserCtx, nil
}

func (c *Capturer) shutdownLocked() {
	if c.browserCancel != nil {
		c.browserCancel()
		c.browserCancel = nil
	}
	if c.allocCancel != nil {
		c.allocCancel()
		c.allocCancel = nil
	}
	c.browserCtx = nil
	c.captureN = 0
}

func findChrome() string {
	if p := strings.TrimSpace(os.Getenv("IF_CHROME_PATH")); p != "" {
		if isRunnableChrome(p) {
			return p
		}
	}
	home := strings.TrimSpace(os.Getenv("HOME"))
	candidates := []string{
		home + "/chrome-for-testing/chrome-headless-shell-linux64/chrome-headless-shell",
		home + "/chrome-for-testing/chrome-linux64/chrome",
		"/home/sprite/chrome-for-testing/chrome-headless-shell-linux64/chrome-headless-shell",
		"/home/sprite/chrome-for-testing/chrome-linux64/chrome",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/snap/bin/chromium",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
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
			strings.Contains(head, "snap install chromium") ||
			(strings.Contains(head, "/snap/bin/chromium") && !fileExists("/snap/bin/chromium")) {
			return false
		}
	}
	return true
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// validatePublicHTTPURL blocks SSRF to private/link-local hosts.
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
