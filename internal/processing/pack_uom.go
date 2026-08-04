package processing

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// PackInfo is a pack / unit-of-issue parse from product text (ETS, title, offer).
type PackInfo struct {
	// PackQuantity is how many base units the listed sell price covers (e.g. 12 for dozen).
	PackQuantity int
	// Unit is a short UOM code: EA, DZ, CS, PK, BX, CT, RM, RL, PR, SET, HD, PG.
	Unit string
	// Label is a short human phrase when useful (e.g. "dozen", "case of 24").
	Label string
	// BaseUnit is the inner unit when known (e.g. "sheet" for ream/sheets).
	BaseUnit string
}

// parsePackUOM extracts pack size and unit-of-issue cues from commercial text.
// Returns zero PackInfo when nothing reliable is found (caller keeps qty=1).
func parsePackUOM(texts ...string) PackInfo {
	blob := strings.Join(texts, " ")
	blob = strings.Join(strings.Fields(blob), " ")
	if blob == "" {
		return PackInfo{}
	}
	lower := strings.ToLower(blob)

	// Try patterns from most specific to least. First confident hit wins.
	type cand struct {
		re   *regexp.Regexp
		qty  func([]string) int
		unit string
		label func([]string) string
		base string
	}

	intGroup := func(i int) func([]string) int {
		return func(m []string) int {
			if i >= len(m) {
				return 0
			}
			n, err := strconv.Atoi(strings.ReplaceAll(m[i], ",", ""))
			if err != nil || n <= 0 || n > 100000 {
				return 0
			}
			return n
		}
	}
	fixed := func(n int) func([]string) int { return func([]string) int { return n } }
	lab := func(s string) func([]string) string { return func([]string) string { return s } }
	labFmt := func(prefix string, i int) func([]string) string {
		return func(m []string) string {
			if i < len(m) {
				return prefix + m[i]
			}
			return prefix
		}
	}

	// Order matters: prefer explicit "N per case" over bare "dozen" in same string when both match —
	// we take the first matching pattern in this list.
	patterns := []cand{
		// 72/Carton, 50/PK, 12/CS, 24/BX, 10/DZ
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*/\s*(cartons?|ctns?|ct)\b`), intGroup(1), "CT", labFmt("", 1), ""},
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*/\s*(cases?|cs)\b`), intGroup(1), "CS", labFmt("per case ", 1), ""},
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*/\s*(packs?|pkgs?|pk|pks)\b`), intGroup(1), "PK", labFmt("per pack ", 1), ""},
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*/\s*(boxes?|bxs?|bx)\b`), intGroup(1), "BX", labFmt("per box ", 1), ""},
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*/\s*(dozens?|doz|dz)\b`), intGroup(1), "DZ", labFmt("", 1), ""},
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*/\s*(reams?|rm)\b`), intGroup(1), "RM", labFmt("", 1), "sheet"},
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*/\s*(rolls?|rls?|rl)\b`), intGroup(1), "RL", labFmt("", 1), ""},
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*/\s*(pairs?|pr)\b`), intGroup(1), "PR", labFmt("", 1), ""},
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*/\s*(each|ea|unit|units)\b`), intGroup(1), "EA", labFmt("", 1), ""},

		// case of 24, pack of 12, box of 10, carton of 72, set of 6
		{regexp.MustCompile(`(?i)\bcase\s+of\s+(\d{1,5})\b`), intGroup(1), "CS", labFmt("case of ", 1), ""},
		{regexp.MustCompile(`(?i)\b(?:pack|package|pkg)\s+of\s+(\d{1,5})\b`), intGroup(1), "PK", labFmt("pack of ", 1), ""},
		{regexp.MustCompile(`(?i)\bbox\s+of\s+(\d{1,5})\b`), intGroup(1), "BX", labFmt("box of ", 1), ""},
		{regexp.MustCompile(`(?i)\bcarton\s+of\s+(\d{1,5})\b`), intGroup(1), "CT", labFmt("carton of ", 1), ""},
		{regexp.MustCompile(`(?i)\bset\s+of\s+(\d{1,5})\b`), intGroup(1), "SET", labFmt("set of ", 1), ""},
		{regexp.MustCompile(`(?i)\bcount\s+of\s+(\d{1,5})\b`), intGroup(1), "PK", labFmt("count of ", 1), ""},

		// 12-pack, 12 pack, 12pk, 24-count, 24 count
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*[- ]?\s*packs?\b`), intGroup(1), "PK", labFmt("", 1), ""},
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*pk\b`), intGroup(1), "PK", labFmt("", 1), ""},
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*[- ]?\s*counts?\b`), intGroup(1), "PK", labFmt("", 1), ""},
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*[- ]?\s*ct\b`), intGroup(1), "PK", labFmt("", 1), ""},

		// multipack / multipack of N
		{regexp.MustCompile(`(?i)\bmulti[-\s]?pack\s+of\s+(\d{1,5})\b`), intGroup(1), "PK", labFmt("multipack of ", 1), ""},
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*[- ]?\s*multi[-\s]?packs?\b`), intGroup(1), "PK", labFmt("", 1), ""},

		// dozen / gross / pair (sell-unit words — before "N sheets" which is often content size)
		{regexp.MustCompile(`(?i)\b(dozens?|doz\.?|dz)\b`), fixed(12), "DZ", lab("dozen"), ""},
		{regexp.MustCompile(`(?i)\bgross\b`), fixed(144), "GR", lab("gross"), ""},
		{regexp.MustCompile(`(?i)\b(pairs?|pr)\b`), fixed(2), "PR", lab("pair"), ""},

		// ream → 500 sheets for paper (common commercial convention)
		{regexp.MustCompile(`(?i)\breams?\b`), fixed(500), "RM", lab("ream"), "sheet"},

		// 50 sheets — content size; only if no stronger sell-unit cue above
		{regexp.MustCompile(`(?i)\b(\d{1,5})\s*[- ]?\s*sheets?\b`), intGroup(1), "EA", labFmt("", 1), "sheet"},

		// Bare unit words without a count (unit only — quantity left for other cues)
		{regexp.MustCompile(`(?i)\b(cases?|cs)\b`), fixed(0), "CS", lab("case"), ""},
		{regexp.MustCompile(`(?i)\b(boxes?|bxs?|bx)\b`), fixed(0), "BX", lab("box"), ""},
		{regexp.MustCompile(`(?i)\b(cartons?|ctns?)\b`), fixed(0), "CT", lab("carton"), ""},
	}

	for _, p := range patterns {
		m := p.re.FindStringSubmatch(lower)
		if m == nil {
			continue
		}
		qty := 0
		if p.qty != nil {
			qty = p.qty(m)
		}
		// Unit-only match (e.g. bare "case") — keep unit, leave qty 0 for caller
		if qty <= 0 && p.unit == "" {
			continue
		}
		if qty <= 0 && p.unit != "" && p.unit != "CS" && p.unit != "BX" && p.unit != "CT" && p.unit != "PK" {
			// For dozen/pair/ream we always have qty from fixed().
			if qty <= 0 {
				continue
			}
		}
		info := PackInfo{
			PackQuantity: qty,
			Unit:         p.unit,
			BaseUnit:     p.base,
		}
		if p.label != nil {
			info.Label = strings.TrimSpace(p.label(m))
		}
		// Bare "case"/"box" without count: record unit only
		if info.PackQuantity <= 0 {
			if info.Unit == "CS" || info.Unit == "BX" || info.Unit == "CT" || info.Unit == "PK" {
				info.PackQuantity = 0
				return info
			}
			continue
		}
		return info
	}

	// Trailing federal U/I style: "… EA", "… DZ" at end of short description.
	// Skip negated phrases ("no pack", "not case").
	if m := regexp.MustCompile(`(?i)\b(ea|each|dz|dozen|cs|case|bx|box|pk|pack|rm|ream|pr|pair|hd|hundred|pg|package)\s*$`).FindStringSubmatch(lower); m != nil {
		tok := strings.ToLower(m[1])
		if strings.HasSuffix(lower, "no "+tok) || strings.HasSuffix(lower, "not "+tok) ||
			strings.Contains(lower, "no "+tok) || strings.Contains(lower, "without "+tok) {
			return PackInfo{}
		}
		switch tok {
		case "ea", "each":
			return PackInfo{PackQuantity: 1, Unit: "EA", Label: "each"}
		case "dz", "dozen":
			return PackInfo{PackQuantity: 12, Unit: "DZ", Label: "dozen"}
		case "cs", "case":
			return PackInfo{Unit: "CS", Label: "case"}
		case "bx", "box":
			return PackInfo{Unit: "BX", Label: "box"}
		case "pk", "pack", "pg", "package":
			return PackInfo{Unit: "PK", Label: "pack"}
		case "rm", "ream":
			return PackInfo{PackQuantity: 500, Unit: "RM", Label: "ream", BaseUnit: "sheet"}
		case "pr", "pair":
			return PackInfo{PackQuantity: 2, Unit: "PR", Label: "pair"}
		case "hd", "hundred":
			return PackInfo{PackQuantity: 100, Unit: "HD", Label: "hundred"}
		}
	}

	return PackInfo{}
}

// enrichMarketOfferPack fills Quantity / Unit / PricePerEach from text when missing.
// unitPrice is the observed listing price for the sell unit (unchanged).
func enrichMarketOfferPack(o *models.MarketOffer, texts ...string) {
	if o == nil {
		return
	}
	// Build text pool: explicit title on offer + caller context
	var pool []string
	if o.Title != "" {
		pool = append(pool, o.Title)
	}
	pool = append(pool, texts...)
	info := parsePackUOM(pool...)

	if o.Quantity <= 0 {
		o.Quantity = 1
	}
	// Only upgrade quantity when we parsed a real pack size > 1 (or explicit 1 each).
	if info.PackQuantity > 0 {
		// Don't shrink an already-richer explicit quantity unless it was default 1.
		if o.Quantity <= 1 || info.PackQuantity > o.Quantity {
			o.Quantity = info.PackQuantity
		}
	}
	if o.Unit == "" && info.Unit != "" {
		o.Unit = info.Unit
	}
	if o.PackLabel == "" && info.Label != "" {
		o.PackLabel = info.Label
	}
	if o.BaseUnit == "" && info.BaseUnit != "" {
		o.BaseUnit = info.BaseUnit
	}
	// Price per base unit when pack > 1
	if o.UnitPrice > 0 && o.Quantity > 1 {
		o.PricePerEach = roundMoney(o.UnitPrice / float64(o.Quantity))
	} else if o.UnitPrice > 0 && o.Quantity == 1 {
		o.PricePerEach = roundMoney(o.UnitPrice)
	}
}

// enrichCommercialMarketOffers applies pack/UOM parse across all offers for a ref
// using ETS description, context, and manufacturer/SKU as weak signals.
func enrichCommercialMarketOffers(r *models.CommercialReference) {
	if r == nil || len(r.MarketOffers) == 0 {
		return
	}
	ctxTexts := []string{
		r.Description,
		r.Context,
		r.Manufacturer,
		r.SKU,
	}
	for i := range r.MarketOffers {
		enrichMarketOfferPack(&r.MarketOffers[i], ctxTexts...)
	}
}
