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
	"sync"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

const (
	defaultPartsBaseAuthURL         = "https://auth.partsbase.com/connect/token"
	defaultPartsBaseBaseURL         = "https://apiservices.partsbase.com"
	defaultPartsBaseGovDataPath     = "/api/data/GovData"
	defaultPartsBaseGovDataType     = "Nsn"
	defaultPartsBaseGovDataStart    = "2000-01-01"
	defaultPartsBaseOAuthGrantType  = "password"
	defaultPartsBaseOAuthScope      = "api"
	defaultPartsBaseTimeoutSeconds  = 30
	defaultPartsBaseTokenExpirySkew = 30 * time.Second
)

// PartsBaseConfig configures the PartsBase extractor.
type PartsBaseConfig struct {
	Enabled          bool
	ClientID         string
	ClientSecret     string
	Username         string
	Password         string
	AuthURL          string
	BaseURL          string
	GovDataPath      string
	GovDataType      string
	GovDataStartDate string
	GovDataSections  []string
	OAuthGrantType   string
	OAuthScope       string
	TimeoutSeconds   int
}

func (cfg PartsBaseConfig) HasCredentials() bool {
	return strings.TrimSpace(cfg.ClientID) != "" &&
		strings.TrimSpace(cfg.ClientSecret) != "" &&
		strings.TrimSpace(cfg.Username) != "" &&
		strings.TrimSpace(cfg.Password) != ""
}

// PartsBaseExtractor pulls live procurement/pricing context from PartsBase GovData.
type PartsBaseExtractor struct {
	clientID         string
	clientSecret     string
	username         string
	password         string
	authURL          string
	baseURL          string
	govDataPath      string
	govDataType      string
	govDataStartDate string
	govDataSections  []string
	oauthGrantType   string
	oauthScope       string
	client           *http.Client

	tokenMu        sync.RWMutex
	accessToken    string
	tokenExpiresAt time.Time
}

func NewPartsBaseExtractor(cfg PartsBaseConfig) *PartsBaseExtractor {
	authURL := strings.TrimSpace(cfg.AuthURL)
	if authURL == "" {
		authURL = defaultPartsBaseAuthURL
	}

	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = defaultPartsBaseBaseURL
	}

	govDataPath := strings.TrimSpace(cfg.GovDataPath)
	if govDataPath == "" {
		govDataPath = defaultPartsBaseGovDataPath
	}
	if !strings.HasPrefix(govDataPath, "/") {
		govDataPath = "/" + govDataPath
	}

	govDataType := strings.TrimSpace(cfg.GovDataType)
	if govDataType == "" {
		govDataType = defaultPartsBaseGovDataType
	}

	govDataStartDate := strings.TrimSpace(cfg.GovDataStartDate)
	if govDataStartDate == "" {
		govDataStartDate = defaultPartsBaseGovDataStart
	}

	sections := normalizeSectionList(cfg.GovDataSections)
	if len(sections) == 0 {
		sections = []string{"Procurement", "NsnId"}
	}

	oauthGrantType := strings.TrimSpace(cfg.OAuthGrantType)
	if oauthGrantType == "" {
		oauthGrantType = defaultPartsBaseOAuthGrantType
	}

	oauthScope := strings.TrimSpace(cfg.OAuthScope)
	if oauthScope == "" {
		oauthScope = defaultPartsBaseOAuthScope
	}

	timeoutSeconds := cfg.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultPartsBaseTimeoutSeconds
	}

	return &PartsBaseExtractor{
		clientID:         strings.TrimSpace(cfg.ClientID),
		clientSecret:     strings.TrimSpace(cfg.ClientSecret),
		username:         strings.TrimSpace(cfg.Username),
		password:         strings.TrimSpace(cfg.Password),
		authURL:          authURL,
		baseURL:          baseURL,
		govDataPath:      govDataPath,
		govDataType:      govDataType,
		govDataStartDate: govDataStartDate,
		govDataSections:  sections,
		oauthGrantType:   oauthGrantType,
		oauthScope:       oauthScope,
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

	if strings.TrimSpace(p.clientID) == "" ||
		strings.TrimSpace(p.clientSecret) == "" ||
		strings.TrimSpace(p.username) == "" ||
		strings.TrimSpace(p.password) == "" {
		return []models.DataSnapshot{}, nil
	}

	query := buildPartsBaseQuery(entityID, params)
	requestURL, err := p.buildGovDataURL(query, params)
	if err != nil {
		snap := p.unavailableSnapshot(entityID, query, "", err)
		return []models.DataSnapshot{snap}, nil
	}

	accessToken, err := p.getAccessToken(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		snap := p.unavailableSnapshot(entityID, query, requestURL, err)
		return []models.DataSnapshot{snap}, nil
	}

	payload, err := p.fetchGovData(ctx, requestURL, accessToken)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		snap := p.unavailableSnapshot(entityID, query, requestURL, err)
		return []models.DataSnapshot{snap}, nil
	}

	signal := normalizePartsBaseGovDataPayload(payload)
	quality := scorePartsBaseQuality(signal)

	snap := models.DataSnapshot{
		EntityID:   entityID,
		SourceCode: p.SourceCode(),
		SnapshotAt: time.Now(),
		RawResponse: map[string]any{
			"query":                  query,
			"request_url":            requestURL,
			"result_count":           signal.ResultCount,
			"supplier_count":         signal.SupplierCount,
			"suppliers":              signal.Suppliers,
			"price_signals":          signal.PriceSignals,
			"commercial_references":  signal.CommercialReferences,
			"last_updated":           signal.LastUpdated,
			"nsn_description":        signal.NSNDescription,
			"data_source":            "live_partsbase_govdata",
			"note":                   "Live PartsBase GovData procurement intelligence for supplier and commercial cross-reference context.",
			"raw_partsbase_response": payload,
		},
		QualityScore: quality,
		CreatedBy:    "partsbase-extractor-v0.2",
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

func (p *PartsBaseExtractor) buildGovDataURL(query string, params map[string]string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(p.baseURL))
	if err != nil {
		return "", fmt.Errorf("invalid partsbase base url: %w", err)
	}

	relative := &url.URL{Path: p.govDataPath}
	endpoint := base.ResolveReference(relative)

	govDataType := p.govDataType
	if params != nil {
		if candidate := strings.TrimSpace(params["partsbase_type"]); candidate != "" {
			govDataType = candidate
		}
	}

	startDate := p.govDataStartDate
	if params != nil {
		if candidate := strings.TrimSpace(params["partsbase_start_date"]); candidate != "" {
			startDate = candidate
		}
	}

	sections := p.govDataSections
	if params != nil {
		if candidate := strings.TrimSpace(params["partsbase_sections"]); candidate != "" {
			override := normalizeSectionList(strings.Split(candidate, ","))
			if len(override) > 0 {
				sections = override
			}
		}
	}

	q := endpoint.Query()
	q.Set("Filter", query)
	q.Set("Type", govDataType)
	if startDate != "" {
		q.Set("startDate", startDate)
	}
	for _, section := range sections {
		q.Add("Section", section)
	}
	endpoint.RawQuery = q.Encode()

	return endpoint.String(), nil
}

func (p *PartsBaseExtractor) getAccessToken(ctx context.Context) (string, error) {
	now := time.Now()

	p.tokenMu.RLock()
	if strings.TrimSpace(p.accessToken) != "" && now.Add(defaultPartsBaseTokenExpirySkew).Before(p.tokenExpiresAt) {
		token := p.accessToken
		p.tokenMu.RUnlock()
		return token, nil
	}
	p.tokenMu.RUnlock()

	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()

	now = time.Now()
	if strings.TrimSpace(p.accessToken) != "" && now.Add(defaultPartsBaseTokenExpirySkew).Before(p.tokenExpiresAt) {
		return p.accessToken, nil
	}

	form := url.Values{}
	form.Set("grant_type", p.oauthGrantType)
	form.Set("client_id", p.clientID)
	form.Set("client_secret", p.clientSecret)
	form.Set("username", p.username)
	form.Set("password", p.password)
	if strings.TrimSpace(p.oauthScope) != "" {
		form.Set("scope", p.oauthScope)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.authURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return "", err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("partsbase auth returned %d: %s", resp.StatusCode, truncateForError(strings.TrimSpace(string(body)), 240))
	}

	var tokenResp oauthTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("decode partsbase auth response: %w", err)
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return "", fmt.Errorf("partsbase auth returned empty access token")
	}

	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	p.accessToken = strings.TrimSpace(tokenResp.AccessToken)
	p.tokenExpiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	return p.accessToken, nil
}

func (p *PartsBaseExtractor) fetchGovData(ctx context.Context, requestURL, accessToken string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

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

	trimmedBody := strings.TrimSpace(string(body))
	if trimmedBody == "" {
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
		return map[string]any{"procurement": arr}, nil
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
			"note":                  "PartsBase GovData was unavailable for this run.",
			"error":                 err.Error(),
		},
		QualityScore: 0.35,
		CreatedBy:    "partsbase-extractor-v0.2",
	}
}

type partsBaseSignal struct {
	ResultCount          int
	SupplierCount        int
	Suppliers            []string
	PriceSignals         []map[string]any
	CommercialReferences []map[string]any
	LastUpdated          string
	NSNDescription       string
}

type oauthTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

func normalizePartsBaseGovDataPayload(payload map[string]any) partsBaseSignal {
	var signal partsBaseSignal
	signal.ResultCount = firstPositiveInt(
		intFromAny(payload["result_count"]),
		intFromAny(payload["resultCount"]),
		intFromAny(payload["total_results"]),
		intFromAny(payload["totalResults"]),
	)
	signal.NSNDescription = extractNSNDescription(payload)

	supplierSet := make(map[string]bool)
	seenPriceSignals := make(map[string]bool)
	seenCommercialRefs := make(map[string]bool)
	lastUpdatedAt := time.Time{}

	procurementRows := mapSliceFromAny(payload["procurement"])
	if len(procurementRows) == 0 {
		procurementRows = mapSliceFromAny(payload["Procurement"])
	}
	for _, row := range procurementRows {
		vendor := firstNonEmptyString(
			row["vendor"],
			row["Vendor"],
			row["supplierName"],
			row["SupplierName"],
			row["sellerCompany"],
			row["SellerCompany"],
		)
		if vendor != "" {
			supplierSet[vendor] = true
		}

		contract := firstNonEmptyString(
			row["contractNo"],
			row["ContractNo"],
			row["contract_number"],
			row["contractNumber"],
		)
		awardDate := firstNonEmptyString(
			row["awardDate"],
			row["AwardDate"],
			row["award_date"],
		)
		if parsed, ok := parsePartsBaseAwardDate(awardDate); ok {
			if lastUpdatedAt.IsZero() || parsed.After(lastUpdatedAt) {
				lastUpdatedAt = parsed
				signal.LastUpdated = parsed.Format("2006-01-02")
			}
		} else if signal.LastUpdated == "" && awardDate != "" {
			signal.LastUpdated = awardDate
		}

		quantity := intFromAny(row["quantity"])
		if quantity == 0 {
			quantity = intFromAny(row["Quantity"])
		}
		if quantity <= 0 {
			quantity = 1
		}

		unitPrice, hasUnitPrice := firstFloat(
			row["unitPrice"],
			row["UnitPrice"],
			row["price"],
			row["Price"],
			row["extendedPrice"],
			row["ExtendedPrice"],
		)
		if hasUnitPrice && unitPrice > 0 {
			priceSignal := map[string]any{
				"unit_price": unitPrice,
				"quantity":   quantity,
			}
			if vendor != "" {
				priceSignal["supplier"] = vendor
			}
			if contract != "" {
				priceSignal["contract_number"] = contract
			}
			if awardDate != "" {
				priceSignal["award_date"] = awardDate
			}
			appendUniqueMapBySignature(&signal.PriceSignals, seenPriceSignals, priceSignal)
		}

		ref := map[string]any{}
		manufacturer := firstNonEmptyString(
			row["manufacturer"],
			row["Manufacturer"],
			row["mfrName"],
			row["MfrName"],
		)
		if manufacturer == "" {
			manufacturer = vendor
		}
		if manufacturer != "" {
			ref["manufacturer"] = manufacturer
		}
		if contract != "" {
			ref["sku"] = contract
		}
		if hasUnitPrice && unitPrice > 0 {
			ref["price"] = formatPriceString(unitPrice)
		}
		var contextBits []string
		contextBits = append(contextBits, "PartsBase GovData procurement")
		if vendor != "" {
			contextBits = append(contextBits, "vendor "+vendor)
		}
		if contract != "" {
			contextBits = append(contextBits, "contract "+contract)
		}
		if awardDate != "" {
			contextBits = append(contextBits, "award "+awardDate)
		}
		ref["context"] = strings.Join(contextBits, " | ")
		appendUniqueMapBySignature(&signal.CommercialReferences, seenCommercialRefs, ref)
	}
	if signal.ResultCount == 0 && len(procurementRows) > 0 {
		signal.ResultCount = len(procurementRows)
	}

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
		ctxParts = append(ctxParts, "PartsBase reference")
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

	if signal.ResultCount == 0 {
		signal.ResultCount = len(signal.PriceSignals)
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

func extractNSNDescription(payload map[string]any) string {
	candidates := mapSliceFromAny(payload["nsnId"])
	if len(candidates) == 0 {
		candidates = mapSliceFromAny(payload["NsnId"])
	}
	for _, candidate := range candidates {
		desc := firstNonEmptyString(candidate["description"], candidate["Description"])
		if desc != "" {
			return desc
		}
	}
	return ""
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

func parsePartsBaseAwardDate(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	layouts := []string{
		"2006-01-02",
		"2006/01/02",
		"2006-01",
		"2006/01",
		time.RFC3339,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
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

func normalizeSectionList(values []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, trimmed)
	}
	return out
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