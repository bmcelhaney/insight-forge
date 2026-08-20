package clickhouse

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client talks to ClickHouse Cloud over HTTPS (port 8443) using the HTTP
// interface. Traffic is expected to egress via the NetBird ch-egress peer.
type Client struct {
	base     string
	database string
	user     string
	password string
	http     *http.Client

	mu      sync.Mutex
	lastOK  bool
	lastErr string
	lastAt  time.Time
}

// Config is the ClickHouse HTTP endpoint. Host may be a URL or hostname.
type Config struct {
	Host     string
	Port     string
	Database string
	User     string
	Password string
}

func New(cfg Config) (*Client, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" || strings.TrimSpace(cfg.User) == "" || strings.TrimSpace(cfg.Password) == "" {
		return nil, fmt.Errorf("clickhouse: host, user, and password are required")
	}
	port := strings.TrimSpace(cfg.Port)
	if port == "" {
		port = "8443"
	}
	db := strings.TrimSpace(cfg.Database)
	if db == "" {
		db = "default"
	}
	base, err := joinHostPort(host, port)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:     base,
		database: db,
		user:     strings.TrimSpace(cfg.User),
		password: cfg.Password,
		http: &http.Client{
			Timeout: 45 * time.Second,
			Transport: &http.Transport{
				Proxy:                 nil, // never honor HTTP_PROXY for CH
				TLSHandshakeTimeout:   8 * time.Second,
				ResponseHeaderTimeout: 15 * time.Second,
				IdleConnTimeout:       60 * time.Second,
			},
		},
	}, nil
}

func joinHostPort(host, port string) (string, error) {
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("clickhouse host: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("clickhouse host is empty")
	}
	if u.Port() == "" {
		u.Host = u.Hostname() + ":" + port
	}
	u.Path = "/"
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

// Configured reports whether this client can be used.
func (c *Client) Configured() bool {
	return c != nil && c.base != "" && c.user != "" && c.password != ""
}

// LastStatus is a lightweight health snapshot (no secrets).
func (c *Client) LastStatus() (ok bool, errText string, at time.Time) {
	if c == nil {
		return false, "not configured", time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastOK, c.lastErr, c.lastAt
}

func (c *Client) record(ok bool, errText string) {
	c.mu.Lock()
	c.lastOK = ok
	c.lastErr = errText
	c.lastAt = time.Now().UTC()
	c.mu.Unlock()
}

// Ping hits /ping (unauthenticated on ClickHouse Cloud still proves TLS+route).
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/ping", nil)
	if err != nil {
		c.record(false, err.Error())
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.record(false, err.Error())
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode != 200 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		c.record(false, msg)
		return fmt.Errorf("clickhouse ping: %s", msg)
	}
	c.record(true, "")
	return nil
}

// ExecJSONEachRow POSTs newline-delimited JSON rows as INSERT ... FORMAT JSONEachRow.
func (c *Client) ExecJSONEachRow(ctx context.Context, table string, payload []byte) error {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	table = strings.TrimSpace(table)
	if table == "" {
		return fmt.Errorf("clickhouse: empty table")
	}
	q := url.Values{}
	q.Set("database", c.database)
	q.Set("query", "INSERT INTO "+table+" FORMAT JSONEachRow")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/?"+q.Encode(), bytes.NewReader(payload))
	if err != nil {
		c.record(false, err.Error())
		return err
	}
	req.SetBasicAuth(c.user, c.password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		c.record(false, err.Error())
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		c.record(false, msg)
		return fmt.Errorf("clickhouse insert %s: %s", table, msg)
	}
	c.record(true, "")
	return nil
}
