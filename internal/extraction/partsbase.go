package extraction

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

const (
	defaultPartsBaseBaseURL          = "https://services.partsbase.com"
	defaultPartsBaseMarketPricingURL = "/api-market-pricing"
	defaultPartsBaseTimeoutSeconds   = 10
)

// PartsBaseConfig configures the PartsBase extractor.
type PartsBaseConfig struct {
	Enabled           bool
	APIKey            string
	BaseURL           string
	MarketPricingPath string
	TimeoutSeconds    int
}

// PartsBaseExtractor pulls live market pricing and supplier context from PartsBase.
type PartsBaseExtractor struct {
	apiKey            string
	baseURL           string
	marketPricingPath string
	client            *http.Client
}

func NewPartsBaseExtractor(cfg PartsBaseConfig) *PartsBaseExtractor {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = defaultPartsBaseBaseURL
	}

	marketPricingPath := strings.TrimSpace(cfg.MarketPricingPath)
	if marketPricingPath == "" {
		marketPricingPath = defaultPartsBaseMarketPricingURL
	}
	if !strings.HasPrefix(marketPricingPath, "/") {
		marketPricingPath = "/" + marketPricingPath
	}

	timeoutSeconds := cfg.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultPartsBaseTimeoutSeconds
	}

	return &PartsBaseExtractor{
		apiKey:            strings.TrimSpace(cfg.APIKey),
		baseURL:           baseURL,
		marketPricingPath: marketPricingPath,
		client: &http.Client{
			Timeout: time.Duration(timeoutSeconds) * time.Second,
		},
	}
}

func (p *PartsBaseExtractor) SourceCode() string { return "PARTSBASE" }

func (p *PartsBaseExtractor) Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if strings.TrimSpace(p.apiKey) == "" {
		return []models.DataSnapshot{}, nil
	}

	query := buildPartsBaseQuery(entityID, params)
	requestURL, err := p.buildMarketPricingURL(query)
	if err != nil {
		snap := p.unavailableSnapshot(entityID, query, "", err)
		return []models.DataSnapshot{snap}, nil
	}

	payload, err := p.fetchMarketPricing(ctx, requestURL)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		snap := p.unavailableSnapshot(entityID, query, requestURL, err)
		return []models.DataSnapshot{snap}, nil
	}

	signal := normalizePartsBasePayload(payload)
	quality := scorePartsBaseQuality(signal)

	snap := models.DataSnapshot{
		EntityID:   entityID,
		SourceCode: p.SourceCode(),
		SnapshotAt: time.Now(),
		RawResponse: map[string]any{
			"query":                 query,
			"request_url":           requestURL,
			"result_count":          signal.ResultCount,
			"supplier_count":        signal.SupplierCount,
			"suppliers":             signal.Suppliers,
			"price_signals":         signal.PriceSignals,
			"commercial_references": signal.CommercialReferences,
			"last_updated":          signal.LastUpdated,
			"data_source":           "live_partsbase_market_pricing",
			"note":                 "Live PartsBase market-pricing intelligence for supplier and commercial cross-reference context.",
			"raw_partsbase_response": payload,
		},
		QualityScore: quality,
		CreatedBy:    "partsbase-extractor-v0.1",
	}

	return []models.DataSnapshot{snap}, nil
}

func buildPartsBaseQuery(entityID string, params map[string]string) string {
	if params != nil {
		if q := strings.TrimSpace(params["partsbase_query"]); q != "" {
			return q
		}
	}
	return strings.TrimSpace(entityID)
}

func (p *PartsBaseExtractor) buildMarketPricingURL(query string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(p.baseURL))
	if err != nil {
		return "", fmt.Errorf("invalid partsbase base url: %w", err)
	}
	relative := &url.URL{Path: p.marketPricingPath}
	endpoint := base.ResolveReference(relative)

	q := endpoint.Query()
	q.Set("partNumber", query)
	q.Set("PartNumber", query)
	endpoint.RawQuery = q.Encode()
	return endpoint.String(), nil
}

func (p *PartsBaseExtractor) fetchMarketPricing(ctx context.Context, requestURL string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("X-API-Key", p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("partsbase returned %d: %s", resp.StatusCode, truncateForError(strings.TrimSpace(string(body)), 240))
	}

	if len(strings.TrimSpace(string(body))) == 0 {
		return map[string]any{}, nil
	}

	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode partsbase response: %w", err)
	}

	if m, ok := decoded.(map[string]any); ok {
		return m, nil
	}
	if arr, ok := decoded.([]any); ok {
		return map[string]any{"results": arr}, nil
	}
	return map[string]any{"value": decoded}, nil
}

func (p *PartsBaseExtractor) unavailableSnapshot(entityID, query, requestURL string, err error) models.DataSnapshot {
	return models.DataSnapshot{
		EntityID:   entityID,
		SourceCode: p.SourceCode(),
		SnapshotAt: time.Now(),
		RawResponse: map[string]any{
			"query":                 query,
			"request_url":           requestURL,
			"result_count":          0,
			"supplier_count":        0,
			"suppliers":             []string{},
			"price_signals":         []map[string]any{},
			"commercial_references": []map[string]any{},
			"data_source":           "partsbase_unavailable",
			"note":                  "PartsBase market-pricing data was unavailable for this run.",
			"error":                 err.Error(),
		},
		QualityScore: 0.35,
		CreatedBy:    "partsbase-extractor-v0.1",
	}
}

type partsBaseSignal struct {
	ResultCount          int
	SupplierCount        int
	Suppliers            []string
	PriceSignals         []map[string]any
	CommercialReferences []map[string]any
	LastUpdated          string
}

func normalizePartsBasePayload(payload map[string]any) partsBaseSignal {
	var signal partsBaseSignal
	signal.ResultCount = firstPositiveInt(
		intFromAny(payload["result_count"]),
		intFromAny(payload["resultCount"]),
		intFromAny(payload["total_results"]),
		intFromAny(payload["totalResults"]),
	)

	supplierSet := make(map[string]bool)
	seenPriceSignals := make(map[string]bool)
	seenCommercialRefs := make(map[string]bool)

	valuesPerCode := mapSliceFromAny(payload["ValuesPerCode"])
	if len(valuesPerCode) == 0 {
		valuesPerCode = mapSliceFromAny(payload["valuesPerCode"])
	}
	if len(valuesPerCode) == 0 {
		valuesPerCode = mapSliceFromAny(payload["values_per_code"])
	}
	for _, row := range valuesPerCode {
		condition := firstNonEmptyString(
			row["ConditionCode"],
			row["conditionCode"],
			row["condition_code"],
		)
		minPrice, hasMinPrice := firstFloat(
			row["MinUnitPrice"],
			row["minUnitPrice"],
			row["min_unit_price"],
		)
		maxPrice, hasMaxPrice := firstFloat(
			row["MaxUnitPrice"],
			row["maxUnitPrice"],
			row["max_unit_price"],
		)
		lastUpdated := firstNonEmptyString(
			row["LastUpdated"],
			row["lastUpdated"],
			row["last_updated"],
		)
		if signal.LastUpdated == "" && lastUpdated != "" {
			signal.LastUpdated = lastUpdated
		}

		priceSignal := map[string]any{}
		if condition != "" {
			priceSignal["condition_code"] = condition
		}
		if hasMinPrice {
			priceSignal["min_unit_price"] = minPrice
		}
		if hasMaxPrice {
			priceSignal["max_unit_price"] = maxPrice
		}
		if lastUpdated != "" {
			priceSignal["last_updated"] = lastUpdated
		}
		appendUniqueMapBySignature(&signal.PriceSignals, seenPriceSignals, priceSignal)
	}
	if signal.ResultCount == 0 && len(valuesPerCode) > 0 {
		signal.ResultCount = len(valuesPerCode)
	}

	rows := collectPartsBaseRows(payload)
	for _, row := range rows {
		supplier := firstNonEmptyString(
			row["SupplierName"],
			row["supplierName"],
			row["supplier_name"],
			row["SellerCompany"],
			row["sellerCompany"],
			row["vendor"],
			row["company"],
		)
		if supplier != "" {
			supplierSet[supplier] = true
		}

		manufacturer := firstNonEmptyString(
			row["Manufacturer"],
			row["manufacturer"],
			row["MfrName"],
			row["mfrName"],
		)
		sku := firstNonEmptyString(
			row["SupplierPartNumber"],
			row["supplierPartNumber"],
			row["supplier_part_number"],
			row["sku"],
			row["mfrPartNumber"],
			row["MfrPartNumber"],
			row["partNumber"],
			row["PartNumber"],
		)
		upc := firstNonEmptyString(
			row["UPC"],
			row["upc"],
			row["GTIN"],
			row["gtin"],
		)
		condition := firstNonEmptyString(
			row["ConditionCode"],
			row["conditionCode"],
			row["condition_code"],
		)

		unitPrice, hasUnitPrice := firstFloat(
			row["UnitPrice"],
			row["unitPrice"],
			row["unit_price"],
			row["price"],
			row["Price"],
			row["MinUnitPrice"],
			row["minUnitPrice"],
		)
		if hasUnitPrice {
			priceSignal := map[string]any{
				"unit_price": unitPrice,
			}
			if condition != "" {
				priceSignal["condition_code"] = condition
			}
			if supplier != "" {
				priceSignal["supplier"] = supplier
			}
			appendUniqueMapBySignature(&signal.PriceSignals, seenPriceSignals, priceSignal)
		}

		ref := map[string]any{}
		if sku != "" {
			ref["sku"] = sku
		}
		if upc != "" {
			ref["upc"] = upc
		}
		if manufacturer != "" {
			ref["manufacturer"] = manufacturer
		}
		if hasUnitPrice {
			ref["price"] = formatPriceString(unitPrice)
		}
		var ctxParts []string
		ctxParts = append(ctxParts, "PartsBase market pricing")
		if condition != "" {
			ctxParts = append(ctxParts, "condition "+condition)
		}
		if supplier != "" {
			ctxParts = append(ctxParts, "supplier "+supplier)
		}
		if len(ctxParts) > 0 {
			ref["context"] = strings.Join(ctxParts, " | ")
		}
		appendUniqueMapBySignature(&signal.CommercialReferences, seenCommercialRefs, ref)
	}

	if signal.ResultCount == 0 && len(rows) > 0 {
		signal.ResultCount = len(rows)
	}

	for supplier := range supplierSet {
		signal.Suppliers = append(signal.Suppliers, supplier)
	}
	sort.Strings(signal.Suppliers)
	signal.SupplierCount = len(signal.Suppliers)

	return signal
}

func collectPartsBaseRows(payload map[string]any) []map[string]any {
	keys := []string{
		"results", "Results",
		"items", "Items",
		"data", "Data",
		"offers", "Offers",
		"listings", "Listings",
		"records", "Records",
		"rows", "Rows",
	}

	var rows []map[string]any
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		if mapped := mapSliceFromAny(value); len(mapped) > 0 {
			rows = append(rows, mapped...)
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			for _, nestedKey := range keys {
				if mapped := mapSliceFromAny(nested[nestedKey]); len(mapped) > 0 {
					rows = append(rows, mapped...)
				}
			}
			if isLikelyPartsBaseRow(nested) {
				rows = append(rows, nested)
			}
		}
	}

	if len(rows) == 0 && isLikelyPartsBaseRow(payload) {
		rows = append(rows, payload)
	}

	return rows
}

func isLikelyPartsBaseRow(m map[string]any) bool {
	return firstNonEmptyString(
		m["PartNumber"],
		m["partNumber"],
		m["SupplierName"],
		m["supplierName"],
		m["UnitPrice"],
		m["unitPrice"],
		m["price"],
	) != ""
}

func mapSliceFromAny(v any) []map[string]any {
	if v == nil {
		return nil
	}
	if out, ok := v.([]map[string]any); ok {
		return out
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, row := range arr {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, m)
	}
	return out
}

func intFromAny(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case float32:
		return int(t)
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(t))
		if err == nil {
			return i
		}
	}
	return 0
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		clean := strings.TrimSpace(strings.TrimPrefix(strings.ReplaceAll(t, ",", ""), "$"))
		if clean == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(clean, 64)
		if err == nil {
			return f, true
		}
	}
	return 0, false
}

func firstFloat(values ...any) (float64, bool) {
	for _, value := range values {
		if f, ok := toFloat(value); ok {
			return f, true
		}
	}
	return 0, false
}

func firstNonEmptyString(values ...any) string {
	for _, value := range values {
		if value == nil {
			continue
		}
		if s, ok := value.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				return s
			}
		}
	}
	return ""
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func appendUniqueMapBySignature(target *[]map[string]any, seen map[string]bool, candidate map[string]any) {
	if len(candidate) == 0 {
		return
	}
	signatureBytes, err := json.Marshal(candidate)
	if err != nil {
		return
	}
	signature := string(signatureBytes)
	if seen[signature] {
		return
	}
	seen[signature] = true
	*target = append(*target, candidate)
}

func formatPriceString(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func truncateForError(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max-3]) + "..."
}

func scorePartsBaseQuality(signal partsBaseSignal) float64 {
	score := 0.45
	switch {
	case signal.ResultCount >= 12:
		score = 0.93
	case signal.ResultCount >= 6:
		score = 0.86
	case signal.ResultCount >= 2:
		score = 0.78
	case signal.ResultCount >= 1:
		score = 0.70
	}

	if signal.SupplierCount >= 3 {
		score += 0.03
	}
	if len(signal.PriceSignals) > 0 {
		score += 0.02
	}
	if score > 0.97 {
		score = 0.97
	}
	return score
}
