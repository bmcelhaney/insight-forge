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
	// Timeout per page navigation+screenshot (default 45s).
	Timeout time.Duration
	// Viewport width/height (default 1280x720).
	Width  int
	Height int
	// ChromePath overrides binary discovery (optional).
	ChromePath string
	// BrowserStartTimeout is how long to wait for Chrome DevTools websocket (default 60s).
	BrowserStartTimeout time.Duration
}

// Capturer takes screenshots of public HTTPS pages (SSRF-safe).
// It keeps a single long-lived Chrome process and opens a tab per capture —
// spawning a new browser per URL is slow and is the main source of
// "websocket url timeout reached" under load on small hosts.
type Capturer struct {
	opts Options

	mu            sync.Mutex
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
}

// NewCapturer builds a capturer. Chrome/Chromium must be installed on the host.
func NewCapturer(opts Options) *Capturer {
	if opts.Timeout <= 0 {
		opts.Timeout = 45 * time.Second
	}
	if opts.Width <= 0 {
		opts.Width = 1280
	}
	if opts.Height <= 0 {
		opts.Height = 720
	}
	if opts.BrowserStartTimeout <= 0 {
		opts.BrowserStartTimeout = 60 * time.Second
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

	// One retry after recycling the browser — covers cold-start races and
	// "websocket url timeout reached" after a hung/zombie Chrome.
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			c.mu.Lock()
			c.shutdownLocked()
			c.mu.Unlock()
			// Brief pause so the OS can release the previous process.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		res, err := c.captureOnce(ctx, pageURL)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !isBrowserStartError(err) {
			// Page-level errors (HTTP2, net::ERR_*, etc.) won't be fixed by restart.
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
		strings.Contains(s, "invalid context") ||
		strings.Contains(s, "context canceled") ||
		strings.Contains(s, "context deadline exceeded") && strings.Contains(s, "browser")
}

func (c *Capturer) captureOnce(ctx context.Context, pageURL string) (*Result, error) {
	browserCtx, err := c.ensureBrowser()
	if err != nil {
		return nil, fmt.Errorf("screenshot capture: %w", err)
	}

	// Tab context inherits the live browser; page timeout is separate so a slow
	// PDP does not tear down the shared Chrome process.
	tabCtx, tabCancel := chromedp.NewContext(browserCtx)
	defer tabCancel()

	timeout := c.opts.Timeout
	tabCtx, timeoutCancel := context.WithTimeout(tabCtx, timeout)
	defer timeoutCancel()

	// Honour caller cancel without killing the shared browser (tab cancel only).
	go func() {
		select {
		case <-ctx.Done():
			tabCancel()
		case <-tabCtx.Done():
		}
	}()

	var buf []byte
	err = chromedp.Run(tabCtx,
		chromedp.EmulateViewport(int64(c.opts.Width), int64(c.opts.Height)),
		chromedp.Navigate(pageURL),
		// Retail sites rarely reach true networkIdle quickly; fixed settle is enough for evidence.
		chromedp.Sleep(2*time.Second),
		chromedp.CaptureScreenshot(&buf),
	)
	if err != nil {
		// If the browser itself died, clear so ensureBrowser recreates next time.
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
// Browser lifetime is independent of per-request contexts.
func (c *Capturer) ensureBrowser() (context.Context, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.browserCtx != nil && c.browserCtx.Err() == nil {
		return c.browserCtx, nil
	}
	c.shutdownLocked()

	startTO := c.opts.BrowserStartTimeout
	// Allocator parent must outlive individual captures — use Background.
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(c.opts.ChromePath),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true),
		// Prefer a normal desktop UA so bot walls are slightly less aggressive.
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 InsightForge/1.0"),
		chromedp.WindowSize(c.opts.Width, c.opts.Height),
		chromedp.WSURLReadTimeout(startTO),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	// Start Chrome now (connect DevTools) so the first capture doesn't absorb start cost
	// into the page timeout, and so we surface start errors clearly.
	startCtx, startCancel := context.WithTimeout(browserCtx, startTO)
	err := chromedp.Run(startCtx)
	startCancel()
	if err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("browser start (%s): %w", c.opts.ChromePath, err)
	}

	c.allocCancel = allocCancel
	c.browserCtx = browserCtx
	c.browserCancel = browserCancel
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
}

func findChrome() string {
	// Explicit override (preferred on sprites: Chrome for Testing / headless shell).
	if p := strings.TrimSpace(os.Getenv("IF_CHROME_PATH")); p != "" {
		if isRunnableChrome(p) {
			return p
		}
	}
	// Common install locations (real binaries first; skip Ubuntu snap stubs).
	home := strings.TrimSpace(os.Getenv("HOME"))
	candidates := []string{
		// Chrome for Testing (installed under home on sprites)
		home + "/chrome-for-testing/chrome-headless-shell-linux64/chrome-headless-shell",
		home + "/chrome-for-testing/chrome-linux64/chrome",
		"/home/sprite/chrome-for-testing/chrome-headless-shell-linux64/chrome-headless-shell",
		"/home/sprite/chrome-for-testing/chrome-linux64/chrome",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/snap/bin/chromium", // real snap binary when installed
		"/usr/bin/chromium",
		// /usr/bin/chromium-browser is often a snap *wrapper* that fails without snap — check last.
		"/usr/bin/chromium-browser",
	}
	for _, p := range candidates {
		if isRunnableChrome(p) {
			return p
		}
	}
	return ""
}

// isRunnableChrome is true for a real Chrome/Chromium binary (not the Ubuntu apt snap stub script).
func isRunnableChrome(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	// Reject the common Ubuntu stub:
	//   #!/bin/sh
	//   ... requires the chromium snap to be installed ...
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	head := string(buf[:n])
	if strings.HasPrefix(head, "#!") {
		// Shell/python wrappers are only OK if they are the real snap launcher that exists.
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
	// Block obvious local names.
	low := strings.ToLower(host)
	if low == "localhost" || strings.HasSuffix(low, ".local") || low == "metadata.google.internal" {
		return fmt.Errorf("screenshot: blocked host")
	}
	// If host is an IP, ensure it's public.
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("screenshot: blocked private ip")
		}
	}
	// Resolve DNS and block private answers (basic SSRF guard).
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
