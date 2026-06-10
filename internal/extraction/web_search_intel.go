package extraction

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

var (
	searchTitleRegex   = regexp.MustCompile(`(?is)<a[^>]*class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	searchSnippetRegex = regexp.MustCompile(`(?is)<a[^>]*class="result__snippet"[^>]*>(.*?)</a>|<div[^>]*class="result__snippet"[^>]*>(.*?)</div>`)
	searchTagRegex     = regexp.MustCompile(`(?is)<[^>]+>`)
	searchSpaceRegex   = regexp.MustCompile(`\s+`)
)

// WebSearchIntelExtractor performs a live web search for non-obvious, external
// procurement intelligence related to the NSN and emits structured signals.
type WebSearchIntelExtractor struct {
	client *http.Client
}

func NewWebSearchIntelExtractor() *WebSearchIntelExtractor {
	return &WebSearchIntelExtractor{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (w *WebSearchIntelExtractor) SourceCode() string { return "WEB_SEARCH_INTEL" }

func (w *WebSearchIntelExtractor) Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error) {
	select {
	case <-time.After(120 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	query := buildWebSearchQuery(entityID, params)
	results, err := w.searchDuckDuckGo(ctx, query)
	if err != nil {
		return []models.DataSnapshot{{
			EntityID:   entityID,
			SourceCode: "WEB_SEARCH_INTEL",
			SnapshotAt: time.Now(),
			RawResponse: map[string]any{
				"query":       query,
				"result_count": 0,
				"results":     []map[string]any{},
				"note":        "Live web search intelligence was unavailable for this run.",
				"error":       err.Error(),
				"data_source": "live_web_search",
			},
			QualityScore: 0.35,
			CreatedBy:    "web-search-intel-extractor-v0.1",
		}}, nil
	}

	distinctDomains := collectDistinctDomains(results)
	procurementDomains := collectProcurementDomains(distinctDomains)
	signalFlags := deriveWebSignalFlags(results, procurementDomains)

	rawResults := make([]map[string]any, 0, len(results))
	for _, r := range results {
		rawResults = append(rawResults, map[string]any{
			"title":   r.Title,
			"url":     r.URL,
			"domain":  r.Domain,
			"snippet": r.Snippet,
		})
	}

	quality := 0.45
	switch {
	case len(results) >= 8:
		quality = 0.92
	case len(results) >= 5:
		quality = 0.84
	case len(results) >= 3:
		quality = 0.76
	case len(results) >= 1:
		quality = 0.66
	}

	snap := models.DataSnapshot{
		EntityID:   entityID,
		SourceCode: "WEB_SEARCH_INTEL",
		SnapshotAt: time.Now(),
		RawResponse: map[string]any{
			"query":               query,
			"result_count":        len(results),
			"results":             rawResults,
			"distinct_domains":    distinctDomains,
			"procurement_domains": procurementDomains,
			"signal_flags":        signalFlags,
			"data_source":         "live_web_search",
			"note":                "Live web intelligence mined from external procurement-related search results.",
		},
		QualityScore: quality,
		CreatedBy:    "web-search-intel-extractor-v0.1",
	}

	return []models.DataSnapshot{snap}, nil
}

type webSearchResult struct {
	Title   string
	URL     string
	Domain  string
	Snippet string
}

func (w *WebSearchIntelExtractor) searchDuckDuckGo(ctx context.Context, query string) ([]webSearchResult, error) {
	endpoint := "https://duckduckgo.com/html/"
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("q", query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; InsightForge/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("duckduckgo returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return parseSearchResults(string(body)), nil
}

func parseSearchResults(doc string) []webSearchResult {
	idxMatches := searchTitleRegex.FindAllStringSubmatchIndex(doc, -1)
	if len(idxMatches) == 0 {
		return nil
	}

	results := make([]webSearchResult, 0, minInt(8, len(idxMatches)))
	for i, m := range idxMatches {
		if len(m) < 6 {
			continue
		}
		rawHref := doc[m[2]:m[3]]
		title := cleanSearchHTML(doc[m[4]:m[5]])
		link := normalizeDuckDuckGoURL(rawHref)
		domain := domainFromURL(link)
		if title == "" || link == "" || domain == "" {
			continue
		}

		start := m[1]
		end := len(doc)
		if i+1 < len(idxMatches) {
			end = idxMatches[i+1][0]
		}
		if end-start > 1800 {
			end = start + 1800
		}
		segment := doc[start:end]
		snippet := extractSnippet(segment)

		results = append(results, webSearchResult{
			Title:   title,
			URL:     link,
			Domain:  domain,
			Snippet: snippet,
		})
		if len(results) >= 8 {
			break
		}
	}
	return results
}

func extractSnippet(segment string) string {
	m := searchSnippetRegex.FindStringSubmatch(segment)
	if len(m) == 0 {
		return ""
	}
	if len(m) > 1 && strings.TrimSpace(m[1]) != "" {
		return cleanSearchHTML(m[1])
	}
	if len(m) > 2 && strings.TrimSpace(m[2]) != "" {
		return cleanSearchHTML(m[2])
	}
	return ""
}

func normalizeDuckDuckGoURL(raw string) string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	if strings.HasPrefix(raw, "/l/") {
		u, err := url.Parse("https://duckduckgo.com" + raw)
		if err == nil {
			target := u.Query().Get("uddg")
			if target != "" {
				decoded, decErr := url.QueryUnescape(target)
				if decErr == nil && strings.TrimSpace(decoded) != "" {
					raw = decoded
				} else {
					raw = target
				}
			}
		}
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return ""
}

func cleanSearchHTML(s string) string {
	s = html.UnescapeString(s)
	s = searchTagRegex.ReplaceAllString(s, " ")
	s = searchSpaceRegex.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func domainFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || strings.TrimSpace(u.Host) == "" {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(u.Host))
	host = strings.TrimPrefix(host, "www.")
	return host
}

func collectDistinctDomains(results []webSearchResult) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(results))
	for _, r := range results {
		if r.Domain == "" || seen[r.Domain] {
			continue
		}
		seen[r.Domain] = true
		out = append(out, r.Domain)
	}
	sort.Strings(out)
	return out
}

func collectProcurementDomains(domains []string) []string {
	isProcDomain := func(d string) bool {
		if strings.HasSuffix(d, ".gov") || strings.HasSuffix(d, ".mil") {
			return true
		}
		keywords := []string{
			"gsaadvantage.gov",
			"usaspending.gov",
			"acquisition.gov",
			"sam.gov",
			"dla.mil",
			"abilityone.gov",
		}
		for _, kw := range keywords {
			if strings.Contains(d, kw) {
				return true
			}
		}
		return false
	}

	var out []string
	for _, d := range domains {
		if isProcDomain(d) {
			out = append(out, d)
		}
	}
	return out
}

func deriveWebSignalFlags(results []webSearchResult, procurementDomains []string) []string {
	var corpus strings.Builder
	for _, r := range results {
		corpus.WriteString(strings.ToLower(r.Title))
		corpus.WriteString(" ")
		corpus.WriteString(strings.ToLower(r.Snippet))
		corpus.WriteString(" ")
	}
	text := corpus.String()

	flags := []string{}
	if len(procurementDomains) > 0 {
		flags = append(flags, "federal_procurement_presence")
	}
	if strings.Contains(text, "abilityone") || strings.Contains(text, "jwod") || strings.Contains(text, "mandatory source") {
		flags = append(flags, "abilityone_compliance_signal")
	}
	if strings.Contains(text, "obsolete") || strings.Contains(text, "discontinued") || strings.Contains(text, "supersede") {
		flags = append(flags, "obsolescence_signal")
	}
	if strings.Contains(text, "lead time") || strings.Contains(text, "backorder") || strings.Contains(text, "stockout") {
		flags = append(flags, "availability_signal")
	}
	if strings.Contains(text, "specification") || strings.Contains(text, "mil-") || strings.Contains(text, "nsn") {
		flags = append(flags, "specification_signal")
	}
	return flags
}

func buildWebSearchQuery(entityID string, params map[string]string) string {
	if params != nil {
		if q := strings.TrimSpace(params["web_query"]); q != "" {
			return q
		}
	}
	digits := digitsOnlyForWebSearch(entityID)
	switch {
	case len(digits) == 13:
		return fmt.Sprintf("\"%s\" NSN federal procurement suppliers lead time", digits)
	case len(digits) == 9:
		return fmt.Sprintf("\"%s\" NIIN NSN procurement suppliers", digits)
	default:
		return fmt.Sprintf("\"%s\" NSN procurement market intelligence", strings.TrimSpace(entityID))
	}
}

func digitsOnlyForWebSearch(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
