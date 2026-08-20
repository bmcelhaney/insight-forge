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

// PlimsPick is a batch of not-yet-analyzed distinct current-month NSNs.
type PlimsPick struct {
	NSNs            []PlimsNSN `json:"nsns"`
	Eligible        int        `json:"eligible"`         // distinct current-month NSNs not yet in nsn_analyses
	AlreadyAnalyzed int        `json:"already_analyzed"` // distinct current-month NSNs already in nsn_analyses
	RemainingAfter  int        `json:"remaining_after"`  // eligible minus this pick
}

const analysesTable = "fair_market_pricing.nsn_analyses"

// LatestPlimsNSNs returns up to limit distinct digit-NSNs from MONTH='Current month',
// newest CREATION_DATE first, skipping NSNs already present in nsn_analyses.
func (c *Client) LatestPlimsNSNs(ctx context.Context, limit int) (PlimsPick, error) {
	pick := PlimsPick{}
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	stats, err := c.plimsProgress(ctx)
	if err != nil {
		return pick, err
	}
	pick.Eligible = stats.eligible
	pick.AlreadyAnalyzed = stats.already
	if stats.eligible == 0 {
		return pick, fmt.Errorf("all current-month LIST_TYPE=B PLIMS NSNs already have nsn_analyses rows")
	}

	sql := latestPlimsSQL(limit)
	raw, err := c.Query(ctx, sql)
	if err != nil {
		return pick, err
	}
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row struct {
			NSN       string `json:"nsn"`
			NSNDashed string `json:"nsn_dashed"`
			ProdName  string `json:"prod_name"`
			Latest    string `json:"latest"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		digits := digitsOnly(firstNonEmpty(row.NSN, row.NSNDashed))
		if !validAnalyzeNSN(digits) || seen[digits] {
			continue
		}
		seen[digits] = true
		item := PlimsNSN{
			NSN:       digits,
			NSNDashed: strings.TrimSpace(row.NSNDashed),
			ProdName:  strings.TrimSpace(row.ProdName),
			LatestRaw: strings.TrimSpace(row.Latest),
		}
		if t, ok := parseCHTime(row.Latest); ok {
			item.Latest = t
		}
		pick.NSNs = append(pick.NSNs, item)
		if len(pick.NSNs) >= limit {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return pick, err
	}
	if len(pick.NSNs) == 0 {
		return pick, fmt.Errorf("no valid unused NSNs in %s with MONTH='Current month' AND LIST_TYPE='B'", plimsProductsTable)
	}
	pick.RemainingAfter = stats.eligible - len(pick.NSNs)
	if pick.RemainingAfter < 0 {
		pick.RemainingAfter = 0
	}
	return pick, nil
}

type plimsProgress struct {
	eligible int
	already  int
}

func (c *Client) plimsProgress(ctx context.Context) (plimsProgress, error) {
	raw, err := c.Query(ctx, plimsProgressSQL())
	if err != nil {
		return plimsProgress{}, err
	}
	line := strings.TrimSpace(string(raw))
	var row struct {
		Eligible int `json:"eligible"`
		Already  int `json:"already_analyzed"`
	}
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		return plimsProgress{}, fmt.Errorf("plims progress: %w", err)
	}
	return plimsProgress{eligible: row.Eligible, already: row.Already}, nil
}

func latestPlimsSQL(limit int) string {
	return fmt.Sprintf(`SELECT
  replaceRegexpAll(ifNull(NSN, ''), '[^0-9]', '') AS nsn,
  any(NSN) AS nsn_dashed,
  any(PROD_NAME) AS prod_name,
  max(CREATION_DATE) AS latest
FROM %s
WHERE MONTH = 'Current month'
  AND LIST_TYPE = 'B'
  AND length(replaceRegexpAll(ifNull(NSN, ''), '[^0-9]', '')) IN (9, 13)
GROUP BY nsn
HAVING nsn NOT IN (SELECT nsn FROM %s WHERE nsn != '')
ORDER BY latest DESC
LIMIT %d
FORMAT JSONEachRow`, plimsProductsTable, analysesTable, limit)
}

func plimsProgressSQL() string {
	return fmt.Sprintf(`SELECT
  countIf(already = 0) AS eligible,
  countIf(already = 1) AS already_analyzed
FROM (
  SELECT
    nsn,
    nsn IN (SELECT nsn FROM %s WHERE nsn != '') AS already
  FROM (
    SELECT replaceRegexpAll(ifNull(NSN, ''), '[^0-9]', '') AS nsn
    FROM %s
    WHERE MONTH = 'Current month'
      AND LIST_TYPE = 'B'
      AND length(replaceRegexpAll(ifNull(NSN, ''), '[^0-9]', '')) IN (9, 13)
    GROUP BY nsn
  )
)
FORMAT JSONEachRow`, analysesTable, plimsProductsTable)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
