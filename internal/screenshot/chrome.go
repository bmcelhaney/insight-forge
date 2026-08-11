package screenshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// captureChrome is the legacy local-browser path. Prefer BackendThum on sprites —
// retail PDPs routinely hang headless Chrome past any sane deadline.
func (c *Capturer) captureChrome(ctx context.Context, pageURL string) (*Result, error) {
	if c.opts.ChromePath == "" {
		return nil, fmt.Errorf("screenshot chrome: binary not found (set IF_CHROME_PATH)")
	}

	timeout := c.opts.Timeout
	if timeout <= 0 {
		timeout = 18 * time.Second
	}

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(c.opts.ChromePath),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true),
		chromedp.WindowSize(c.opts.Width, c.opts.Height),
		chromedp.WSURLReadTimeout(30*time.Second),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer allocCancel()

	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()

	startDone := make(chan error, 1)
	go func() { startDone <- chromedp.Run(browserCtx) }()
	select {
	case err := <-startDone:
		if err != nil {
			return nil, fmt.Errorf("screenshot chrome: start: %w", err)
		}
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("screenshot chrome: start timed out")
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	tabCtx, tabCancel := chromedp.NewContext(browserCtx)
	defer tabCancel()
	tabCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()

	var buf []byte
	err := chromedp.Run(tabCtx,
		chromedp.EmulateViewport(int64(c.opts.Width), int64(c.opts.Height)),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, _, _, _, e := page.Navigate(pageURL).Do(ctx)
			return e
		}),
		chromedp.Sleep(2*time.Second),
		chromedp.CaptureScreenshot(&buf),
	)
	if err != nil {
		return nil, fmt.Errorf("screenshot chrome: %w", err)
	}
	if len(buf) < 100 {
		return nil, fmt.Errorf("screenshot chrome: empty image")
	}
	sum := sha256.Sum256(buf)
	return &Result{
		PNG:         buf,
		ContentType: "image/png",
		Width:       c.opts.Width,
		Height:      c.opts.Height,
		SHA256:      hex.EncodeToString(sum[:]),
		Backend:     BackendChrome,
	}, nil
}
