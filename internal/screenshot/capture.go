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
	// Timeout per page (default 20s).
	Timeout time.Duration
	// Viewport width/height (default 1280x720).
	Width  int
	Height int
	// ChromePath overrides binary discovery (optional).
	ChromePath string
}

// Capturer takes screenshots of public HTTPS pages (SSRF-safe).
type Capturer struct {
	opts Options
}

// NewCapturer builds a capturer. Chrome/Chromium must be installed on the host.
func NewCapturer(opts Options) *Capturer {
	if opts.Timeout <= 0 {
		opts.Timeout = 20 * time.Second
	}
	if opts.Width <= 0 {
		opts.Width = 1280
	}
	if opts.Height <= 0 {
		opts.Height = 720
	}
	if opts.ChromePath == "" {
		opts.ChromePath = findChrome()
	}
	return &Capturer{opts: opts}
}

// Available reports whether a Chrome binary was found.
func (c *Capturer) Available() bool {
	return c != nil && c.opts.ChromePath != ""
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

	timeout := c.opts.Timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(c.opts.ChromePath),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.WindowSize(c.opts.Width, c.opts.Height),
		chromedp.UserAgent("InsightForge/1.0 (+pricing-evidence-capture)"),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer allocCancel()

	taskCtx, taskCancel := chromedp.NewContext(allocCtx)
	defer taskCancel()

	var buf []byte
	err := chromedp.Run(taskCtx,
		chromedp.EmulateViewport(int64(c.opts.Width), int64(c.opts.Height)),
		chromedp.Navigate(pageURL),
		// Wait for DOM; retail sites rarely reach true networkIdle quickly.
		chromedp.Sleep(2*time.Second),
		chromedp.CaptureScreenshot(&buf),
	)
	if err != nil {
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

func findChrome() string {
	// Explicit override.
	if p := strings.TrimSpace(os.Getenv("IF_CHROME_PATH")); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/snap/bin/chromium",
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
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
