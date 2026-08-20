package clickhouse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const plimsProductsTable = "EBS.XXSC_XXSC_PLIMS_PRODUCTS"

// PlimsNSN is one unique NSN taken from the latest PLIMS product row.
type PlimsNSN struct {
	NSN       string    `json:"nsn"`
	NSNDashed string    `json:"nsn_dashed,omitempty"`
	ProdName  string    `json:"prod_name,omitempty"`
	Latest    time.Time `json:"latest,omitempty"`
	LatestRaw string    `json:"latest_raw,omitempty"`
}

// LatestPlimsNSNs returns up to limit distinct NSNs, newest CREATION_DATE first.
func (c *Client) LatestPlimsNSNs(ctx context.Context, limit int) ([]PlimsNSN, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	// Fetch extra rows so we can skip non-numeric / invalid NSNs.
	fetch := limit * 4
	if fetch < 40 {
		fetch = 40
	}
	if fetch > 200 {
		fetch = 200
	}
	sql := fmt.Sprintf(
		"SELECT NSN AS nsn_dashed, any(PROD_NAME) AS prod_name, max(CREATION_DATE) AS latest "+
			"FROM %s GROUP BY NSN ORDER BY latest DESC LIMIT %d FORMAT JSONEachRow",
		plimsProductsTable, fetch,
	)
	raw, err := c.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	var out []PlimsNSN
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row struct {
			NSNDashed string `json:"nsn_dashed"`
			ProdName  string `json:"prod_name"`
			Latest    string `json:"latest"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		digits := digitsOnly(row.NSNDashed)
		if !validAnalyzeNSN(digits) {
			continue
		}
		item := PlimsNSN{
			NSN:       digits,
			NSNDashed: strings.TrimSpace(row.NSNDashed),
			ProdName:  strings.TrimSpace(row.ProdName),
			LatestRaw: strings.TrimSpace(row.Latest),
		}
		if t, ok := parseCHTime(row.Latest); ok {
			item.Latest = t
		}
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid NSNs in %s", plimsProductsTable)
	}
	return out, nil
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if len(d) > 13 {
		d = d[len(d)-13:]
	}
	return d
}

func validAnalyzeNSN(d string) bool {
	return len(d) == 9 || len(d) == 13
}

func parseCHTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if i := strings.IndexByte(s, '.'); i > 0 {
		s = s[:i]
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		"2006-01-02T15:04:05",
	} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
