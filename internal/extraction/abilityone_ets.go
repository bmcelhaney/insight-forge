package extraction

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bmcelhaney/insight-forge/internal/models"
	"github.com/xuri/excelize/v2"
)

const defaultAbilityOneETSPath = "./docs/20260701 AbilityOne ETS File.xlsx"

type abilityOneETSRow struct {
	SKU                   string
	DateAdded             string
	CommercialUPC         string
	CommercialDescription string
	Manufacturer          string
	AbilityOneNSN         string
	AbilityOneNSNNoDashes string
	NSNPlus7              string
	AbilityOneDescription string
}

func (r abilityOneETSRow) uniqueKey() string {
	return strings.Join([]string{
		r.AbilityOneNSNNoDashes,
		r.SKU,
		r.CommercialUPC,
		r.Manufacturer,
		r.CommercialDescription,
		r.AbilityOneDescription,
	}, "|")
}

// AbilityOneETSExtractor enriches NSN analysis from the AbilityOne ETS spreadsheet
// (SKU/UPC/manufacturer/description cross-reference data).
type AbilityOneETSExtractor struct {
	path      string
	sheetName string
	loadedAt  time.Time
	loadErr   error
	rowCount  int

	byNSN13 map[string][]abilityOneETSRow
	byNIIN9 map[string][]abilityOneETSRow
	byNSN7  map[string][]abilityOneETSRow
}

func NewAbilityOneETSExtractor(path string) *AbilityOneETSExtractor {
	if strings.TrimSpace(path) == "" {
		if envPath := strings.TrimSpace(os.Getenv("IF_ETS_XLSX_PATH")); envPath != "" {
			path = envPath
		} else {
			path = defaultAbilityOneETSPath
		}
	}

	e := &AbilityOneETSExtractor{
		path:    path,
		byNSN13: make(map[string][]abilityOneETSRow),
		byNIIN9: make(map[string][]abilityOneETSRow),
		byNSN7:  make(map[string][]abilityOneETSRow),
	}
	e.load()
	return e
}

func (e *AbilityOneETSExtractor) SourceCode() string { return "ABILITYONE_ETS" }

func (e *AbilityOneETSExtractor) Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if e.loadErr != nil {
		// Graceful degradation: if the file is unavailable or unreadable,
		// don't fail the entire analysis run.
		return []models.DataSnapshot{}, nil
	}

	matches, strategy := e.lookup(entityID)
	if len(matches) == 0 {
		return []models.DataSnapshot{}, nil
	}

	seenRows := make(map[string]bool)
	manufacturerCounts := make(map[string]int)
	abilityDescCounts := make(map[string]int)
	commercialDescCounts := make(map[string]int)
	uniqueManufacturers := make(map[string]bool)
	uniqueSKUs := make(map[string]bool)
	uniqueUPCs := make(map[string]bool)
	var earliestDate time.Time
	var latestDate time.Time
	hasEarliest := false
	hasLatest := false
	recentAdditions12m := 0
	recentAdditions24m := 0
	now := time.Now()
	references := make([]map[string]any, 0, len(matches))

	for _, row := range matches {
		key := row.uniqueKey()
		if seenRows[key] {
			continue
		}
		seenRows[key] = true

		if row.Manufacturer != "" {
			manufacturerCounts[row.Manufacturer]++
			uniqueManufacturers[row.Manufacturer] = true
		}
		if row.AbilityOneDescription != "" {
			abilityDescCounts[row.AbilityOneDescription]++
		}
		if row.CommercialDescription != "" {
			commercialDescCounts[row.CommercialDescription]++
		}
		if row.SKU != "" {
			uniqueSKUs[row.SKU] = true
		}
		if row.CommercialUPC != "" {
			uniqueUPCs[row.CommercialUPC] = true
		}
		if parsed, ok := parseETSDate(row.DateAdded); ok {
			if !hasEarliest || parsed.Before(earliestDate) {
				earliestDate = parsed
				hasEarliest = true
			}
			if !hasLatest || parsed.After(latestDate) {
				latestDate = parsed
				hasLatest = true
			}
			if !parsed.Before(now.AddDate(-1, 0, 0)) {
				recentAdditions12m++
			}
			if !parsed.Before(now.AddDate(-2, 0, 0)) {
				recentAdditions24m++
			}
		}

		ref := map[string]any{
			"source":  e.SourceCode(),
			"context": "AbilityOne ETS spreadsheet cross-reference",
		}
		if row.SKU != "" {
			ref["sku"] = row.SKU
		}
		if row.CommercialUPC != "" {
			ref["upc"] = row.CommercialUPC
		}
		if row.Manufacturer != "" {
			ref["manufacturer"] = row.Manufacturer
		}
		if row.CommercialDescription != "" {
			ref["commercial_description"] = row.CommercialDescription
		}
		if row.AbilityOneDescription != "" {
			ref["abilityone_description"] = row.AbilityOneDescription
		}
		if row.DateAdded != "" {
			ref["date_added"] = row.DateAdded
		}
		if row.AbilityOneNSNNoDashes != "" {
			ref["abilityone_nsn"] = row.AbilityOneNSNNoDashes
		}
		if row.NSNPlus7 != "" {
			ref["nsn_plus_7"] = row.NSNPlus7
		}

		if row.SKU != "" || row.CommercialUPC != "" {
			references = append(references, ref)
		}
	}

	// Keep payloads bounded for UI responsiveness.
	if len(references) > 75 {
		references = references[:75]
	}
	earliestDateText := ""
	latestDateText := ""
	if hasEarliest {
		earliestDateText = earliestDate.Format("2006-01-02")
	}
	if hasLatest {
		latestDateText = latestDate.Format("2006-01-02")
	}
	mappingTrend := inferETSTrend(recentAdditions12m, recentAdditions24m)

	snap := models.DataSnapshot{
		EntityID:   entityID,
		SourceCode: e.SourceCode(),
		SnapshotAt: time.Now(),
		RawResponse: map[string]any{
			"dataset_name":            "20260701 AbilityOne ETS File.xlsx",
			"dataset_sheet":           e.sheetName,
			"dataset_loaded_at":       e.loadedAt.UTC().Format(time.RFC3339),
			"matched_rows_count":      len(seenRows),
			"match_strategy":          strategy,
			"manufacturers":           topValuesByCount(manufacturerCounts, 12),
			"manufacturer_reference_counts": manufacturerCounts,
			"unique_manufacturer_count":     len(uniqueManufacturers),
			"unique_sku_count":              len(uniqueSKUs),
			"unique_upc_count":              len(uniqueUPCs),
			"earliest_date_added":           earliestDateText,
			"latest_date_added":             latestDateText,
			"recent_additions_12m":          recentAdditions12m,
			"recent_additions_24m":          recentAdditions24m,
			"mapping_trend":                 mappingTrend,
			"abilityone_descriptions": topValuesByCount(abilityDescCounts, 12),
			"commercial_descriptions": topValuesByCount(commercialDescCounts, 12),
			"commercial_references":   references,
			"note":                    "AbilityOne ETS cross-reference data matched by NSN/NIIN and merged into commercial reference evidence.",
		},
		QualityScore: 0.98,
		CreatedBy:    "abilityone-ets-extractor-v0.2",
	}

	return []models.DataSnapshot{snap}, nil
}

func (e *AbilityOneETSExtractor) load() {
	f, err := excelize.OpenFile(e.path)
	if err != nil {
		e.loadErr = fmt.Errorf("open AbilityOne ETS file %q: %w", e.path, err)
		return
	}
	defer f.Close()

	sheet := f.GetSheetName(0)
	if sheet == "" {
		e.loadErr = fmt.Errorf("AbilityOne ETS file %q has no sheets", e.path)
		return
	}

	rows, err := f.Rows(sheet)
	if err != nil {
		e.loadErr = fmt.Errorf("open sheet rows for %q: %w", sheet, err)
		return
	}
	defer rows.Close()

	rowNum := 0
	for rows.Next() {
		cols, err := rows.Columns()
		if err != nil {
			continue
		}
		rowNum++
		if rowNum == 1 {
			// Header row.
			continue
		}

		row := abilityOneETSRow{
			SKU:                   strings.TrimSpace(column(cols, 0)),
			DateAdded:             strings.TrimSpace(column(cols, 1)),
			CommercialUPC:         digitsOnly(column(cols, 2)),
			CommercialDescription: strings.TrimSpace(column(cols, 3)),
			Manufacturer:          strings.TrimSpace(column(cols, 4)),
			AbilityOneNSN:         strings.TrimSpace(column(cols, 5)),
			AbilityOneDescription: strings.TrimSpace(column(cols, 8)),
		}

		nsn13 := digitsOnly(column(cols, 6))
		if len(nsn13) != 13 {
			// Fallback to dashed NSN column if needed.
			dashed := digitsOnly(row.AbilityOneNSN)
			if len(dashed) >= 13 {
				nsn13 = dashed[len(dashed)-13:]
			}
		}
		if len(nsn13) != 13 {
			continue
		}
		row.AbilityOneNSNNoDashes = nsn13
		if row.AbilityOneNSN == "" {
			row.AbilityOneNSN = formatNSN13(nsn13)
		}

		row.NSNPlus7 = normalizeNSN7(column(cols, 7), nsn13)

		e.byNSN13[nsn13] = append(e.byNSN13[nsn13], row)
		niin := nsn13[4:]
		e.byNIIN9[niin] = append(e.byNIIN9[niin], row)
		e.byNSN7[niin[len(niin)-7:]] = append(e.byNSN7[niin[len(niin)-7:]], row)
		if row.NSNPlus7 != "" {
			e.byNSN7[row.NSNPlus7] = append(e.byNSN7[row.NSNPlus7], row)
		}

		e.rowCount++
	}

	if e.rowCount == 0 {
		e.loadErr = fmt.Errorf("no AbilityOne ETS rows indexed from %q", e.path)
		return
	}
	e.sheetName = sheet
	e.loadedAt = time.Now()
}

func (e *AbilityOneETSExtractor) lookup(entityID string) ([]abilityOneETSRow, string) {
	digits := digitsOnly(entityID)
	if digits == "" {
		return nil, "none"
	}

	seen := make(map[string]bool)
	var out []abilityOneETSRow
	var strategies []string

	appendRows := func(strategy string, rows []abilityOneETSRow) {
		if len(rows) == 0 {
			return
		}
		strategies = append(strategies, strategy)
		for _, row := range rows {
			key := row.uniqueKey()
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, row)
		}
	}

	if len(digits) >= 13 {
		nsn13 := digits[len(digits)-13:]
		appendRows("nsn13", e.byNSN13[nsn13])
		niin := nsn13[4:]
		appendRows("niin9", e.byNIIN9[niin])
		appendRows("nsn7", e.byNSN7[niin[len(niin)-7:]])
	} else if len(digits) == 9 {
		appendRows("niin9", e.byNIIN9[digits])
		appendRows("nsn7", e.byNSN7[digits[len(digits)-7:]])
	} else if len(digits) == 7 {
		appendRows("nsn7", e.byNSN7[digits])
	} else if len(digits) > 9 {
		appendRows("niin9-suffix", e.byNIIN9[digits[len(digits)-9:]])
	}

	strategy := "none"
	if len(strategies) > 0 {
		strategy = strings.Join(strategies, ",")
	}
	return out, strategy
}

func topValuesByCount(counts map[string]int, limit int) []string {
	type kv struct {
		Key   string
		Count int
	}
	var pairs []kv
	for key, count := range counts {
		if strings.TrimSpace(key) == "" {
			continue
		}
		pairs = append(pairs, kv{Key: key, Count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Count == pairs[j].Count {
			return pairs[i].Key < pairs[j].Key
		}
		return pairs[i].Count > pairs[j].Count
	})
	if len(pairs) > limit {
		pairs = pairs[:limit]
	}
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p.Key)
	}
	return out
}

func column(cols []string, idx int) string {
	if idx < 0 || idx >= len(cols) {
		return ""
	}
	return cols[idx]
}

func normalizeNSN7(raw, nsn13 string) string {
	digits := digitsOnly(raw)
	if len(digits) >= 7 {
		return digits[len(digits)-7:]
	}
	if len(nsn13) == 13 {
		return nsn13[len(nsn13)-7:]
	}
	return ""
}

func formatNSN13(nsn13 string) string {
	if len(nsn13) != 13 {
		return nsn13
	}
	return fmt.Sprintf("%s-%s-%s", nsn13[:4], nsn13[4:6], nsn13[6:])
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parseETSDate(raw string) (time.Time, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{
		"1/2/2006",
		"01/02/2006",
		"1/2/06",
		"2006-01-02",
		"01-02-2006",
		"Jan 2 2006",
		"January 2 2006",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			return parsed, true
		}
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		if parsed, err := excelize.ExcelDateToTime(n, false); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func inferETSTrend(recent12m, recent24m int) string {
	switch {
	case recent12m >= 15:
		return "expanding"
	case recent12m > 0 || recent24m > 0:
		return "stable"
	default:
		return "mature"
	}
}
