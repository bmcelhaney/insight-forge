package clickhouse

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Query runs a ClickHouse SQL statement (POST body) and returns the raw response.
// Use fully-qualified table names (e.g. EBS.XXSC_XXSC_PLIMS_PRODUCTS).
func (c *Client) Query(ctx context.Context, sql string) ([]byte, error) {
	if c == nil || !c.Configured() {
		return nil, fmt.Errorf("clickhouse: not configured")
	}
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return nil, fmt.Errorf("clickhouse: empty query")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/", strings.NewReader(sql))
	if err != nil {
		c.record(false, err.Error())
		return nil, err
	}
	req.SetBasicAuth(c.user, c.password)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp, err := c.http.Do(req)
	if err != nil {
		c.record(false, err.Error())
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		c.record(false, err.Error())
		return nil, err
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		c.record(false, msg)
		return nil, fmt.Errorf("clickhouse query: %s", msg)
	}
	c.record(true, "")
	return body, nil
}
