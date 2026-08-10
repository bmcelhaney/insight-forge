package processing

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
	"github.com/google/uuid"
)

// DataCaptureMeta supplies build identity for the data-capture document.
type DataCaptureMeta struct {
	Commit    string
	BuildTime string
}

// BuildDataCaptureDocument assembles a machine-readable hit inventory from a
// synthesized InsightResult and the underlying snapshots. Suitable as an input
// payload for other applications (not the pricing-tool narrative export).
//
// includePartsBaseInDataCapture controls whether PartsBase historical price
// signals appear in the data-capture document (API + UI Export). Off for now;
// analysis UI still uses PartsBase. Flip to true to restore full export.
const includePartsBaseInDataCapture = false

// Pricing policy (schema 1.1+): every price is an atomic observation with
// unit_price + quantity. Analysis UI ranges are not exported.
// Links policy (schema 1.2+): at most one primary evidence URL per hit
// (links.url + links.url_kind). Multi-channel link bags are not exported.
// Schema 1.3: analysis_id for Tigris proof objects; screenshots attached optionally
// after build via screenshot.AttachProofs.
// When includePartsBaseInDataCapture is true, PartsBase signals come from the
// live snapshot in full (not the 25-row UI sample).
func BuildDataCaptureDocument(result models.InsightResult, snaps []models.DataSnapshot, meta DataCaptureMeta) models.DataCaptureDocument {
	nsn := digitsOnlyString(firstNonEmpty(result.EntityID, ""))
	if len(nsn) > 13 {
		nsn = nsn[len(nsn)-13:]
	}
	niin, fsc := "", ""
	if len(nsn) == 13 {
		fsc = nsn[:4]
		niin = nsn[4:]
	} else if len(nsn) == 9 {
		niin = nsn
	}

	doc := models.DataCaptureDocument{
		Schema:        models.DataCaptureSchemaID,
		SchemaVersion: models.DataCaptureSchemaVersion,
		Purpose: "Machine-readable inventory of NSN/SKU/UPC/ETS/commercial/procurement hits for downstream applications. " +
			"Price hits are atomic (unit_price + quantity); no market ranges. " +
			"Each hit has at most one primary evidence URL (links.url). " +
			"Optional proof.screenshot stores visual capture of that URL in object storage. " +
			"Not the pricing-tool narrative export.",
		ExportedAt: time.Now().UTC(),
		AnalysisID: uuid.NewString(),
		Generator: models.DataCaptureGenerator{
			Name:        "insight-forge",
			Commit:      meta.Commit,
			BuildTime:   meta.BuildTime,
			GeneratedBy: result.GeneratedBy,
		},
		Query: models.DataCaptureQuery{
			NSN:       nsn,
			NSNDashed: formatDashedNSNLocal(nsn),
			NIIN:      niin,
			FSC:       fsc,
			EntityID:  result.EntityID,
		},
		Item: models.DataCaptureItem{
			Name:                     result.ItemName,
			UnitOfIssue:              result.UnitOfIssue,
			TechnicalCharacteristics: result.TechnicalCharacteristics,
		},
		Hits:    make([]models.DataCaptureHit, 0, 64),
		Sources: buildDataCaptureSources(snaps),
	}

	// 1) Item-master identity (no price)
	if result.ItemName != "" || result.TechnicalCharacteristics != "" {
		doc.Hits = append(doc.Hits, models.DataCaptureHit{
			HitID:   "item-master-1",
			HitType: "item_master",
			Source:  "SYNTHESIS",
			Identifiers: models.DataCaptureIdentifiers{
				NSN:  nsn,
				NIIN: niin,
				FSC:  fsc,
			},
			Description: firstNonEmpty(result.ItemName, result.TechnicalCharacteristics),
			Attributes: map[string]any{
				"unit_of_issue": result.UnitOfIssue,
			},
		})
	}

	// 2) Commercial / ETS / GSA product identity hits + atomic price_observation rows
	priceSeq := 0
	for i, c := range result.CommercialReferences {
		hitType := commercialHitType(c.Source)
		parentID := fmt.Sprintf("%s-%d", hitType, i+1)
		ids := models.DataCaptureIdentifiers{
			NSN:          nsn,
			NIIN:         niin,
			FSC:          fsc,
			SKU:          strings.TrimSpace(c.SKU),
			UPC:          digitsOnlyString(c.UPC),
			GTIN:         digitsOnlyString(c.GTIN),
			Manufacturer: strings.TrimSpace(c.Manufacturer),
		}
		hit := models.DataCaptureHit{
			HitID:       parentID,
			HitType:     hitType,
			Source:      firstNonEmpty(c.Source, "COMMERCIAL"),
			Identifiers: ids,
			Description: strings.TrimSpace(c.Description),
			Context:     strings.TrimSpace(c.Context),
			DateAdded:   strings.TrimSpace(c.DateAdded),
			// Identity hits intentionally omit Pricing (ranges live only in UI/analysis).
		}
		if lnk := bestCommercialEvidenceLinks(c); lnk != nil {
			hit.Links = lnk
		}
		doc.Hits = append(doc.Hits, hit)

		// Prefer resolved MarketOffers (one row per observed offer).
		// Skip channel=federal here — AbilityOne list price is emitted once below
		// (was previously duplicated onto every commercial row).
		offers := c.MarketOffers
		if len(offers) == 0 {
			// Fallback: only single (non-range) channel prices.
			offers = singlePriceMarketOffers(c, c.PriceAsOf)
		}
		for _, o := range offers {
			if o.UnitPrice <= 0 {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(o.Channel), "federal") {
				continue
			}
			qty := o.Quantity
			if qty <= 0 {
				qty = 1
			}
			priceSeq++
			cur := o.Currency
			if cur == "" {
				cur = "USD"
			}
			ppe := o.PricePerEach
			if ppe <= 0 && o.UnitPrice > 0 && qty > 0 {
				ppe = roundMoney(o.UnitPrice / float64(qty))
			}
			priceHit := models.DataCaptureHit{
				HitID:       fmt.Sprintf("price-obs-%d", priceSeq),
				HitType:     "price_observation",
				Source:      firstNonEmpty(o.Source, c.Source, "MARKET"),
				Identifiers: ids,
				Description: strings.TrimSpace(c.Description),
				Pricing: &models.DataCapturePricing{
					UnitPrice:    roundMoney(o.UnitPrice),
					Quantity:     qty,
					PricePerEach: ppe,
					Unit:         o.Unit,
					PackLabel:    o.PackLabel,
					BaseUnit:     o.BaseUnit,
					Currency:     cur,
					Channel:      o.Channel,
					Merchant:     o.Merchant,
					PriceSource:  firstNonEmpty(o.Source, c.PriceSource),
					AsOf:         firstNonEmpty(o.AsOf, c.PriceAsOf),
				},
				Attributes: map[string]any{
					"parent_hit_id": parentID,
					"parent_type":   hitType,
				},
			}
			if o.Title != "" {
				if priceHit.Attributes == nil {
					priceHit.Attributes = map[string]any{}
				}
				priceHit.Attributes["offer_title"] = o.Title
			}
			if lnk := bestPriceObservationLinks(o, c); lnk != nil {
				priceHit.Links = lnk
			}
			doc.Hits = append(doc.Hits, priceHit)
		}
	}

	// 3) AbilityOne.com NSN channel price — atomic federal observation
	if ao := result.AbilityOneChannelPrice; ao != nil {
		if up, ok := parseSingleUnitPrice(ao.Price); ok {
			priceSeq++
			doc.Hits = append(doc.Hits, models.DataCaptureHit{
				HitID:   fmt.Sprintf("price-obs-%d", priceSeq),
				HitType: "price_observation",
				Source:  firstNonEmpty(ao.Source, "ABILITYONE_COM"),
				Identifiers: models.DataCaptureIdentifiers{
					NSN:   nsn,
					NIIN:  niin,
					FSC:   fsc,
					SKU:   strings.TrimSpace(ao.SKU),
					Brand: strings.TrimSpace(ao.Brand),
				},
				Description: strings.TrimSpace(ao.Name),
				Pricing: &models.DataCapturePricing{
					UnitPrice:   roundMoney(up),
					Quantity:    1,
					Currency:    "USD",
					Channel:     "federal",
					Merchant:    "AbilityOne.com",
					PriceSource: firstNonEmpty(ao.Source, "ABILITYONE_COM"),
					AsOf:        ao.AsOf,
				},
				Links:   singleEvidenceLink(ao.URL, "federal"),
				Context: strings.TrimSpace(ao.Note),
				Attributes: map[string]any{
					"catalog": "abilityone.com",
				},
			})
		}
	}

	// 4) PartsBase historical transactions (optional — off for now).
	if includePartsBaseInDataCapture {
		if pbHits, total := partsBasePriceHitsFromSnapshots(snaps, nsn, niin, fsc, &priceSeq); len(pbHits) > 0 || total > 0 {
			pbExported := len(pbHits)
			pbTotal := total
			if pb := result.PartsBaseHistoricalPricing; pb != nil || total > 0 {
				src := "PARTSBASE"
				note := "Historical federal procurement unit prices from PartsBase GovData."
				supCount := 0
				lastUp := ""
				if pb != nil {
					src = firstNonEmpty(pb.Source, src)
					if strings.TrimSpace(pb.Note) != "" {
						note = pb.Note
					}
					supCount = pb.SupplierCount
					lastUp = pb.LastUpdated
					if pb.SignalCount > pbTotal {
						pbTotal = pb.SignalCount
					}
				}
				doc.Hits = append(doc.Hits, models.DataCaptureHit{
					HitID:   "partsbase-summary-1",
					HitType: "partsbase_summary",
					Source:  src,
					Identifiers: models.DataCaptureIdentifiers{
						NSN:  nsn,
						NIIN: niin,
						FSC:  fsc,
					},
					Context: note,
					Attributes: map[string]any{
						"signal_count":   pbTotal,
						"exported_count": pbExported,
						"truncated":     false,
						"supplier_count": supCount,
						"last_updated":   lastUp,
					},
				})
			}
			doc.Hits = append(doc.Hits, pbHits...)
		} else if pb := result.PartsBaseHistoricalPricing; pb != nil {
			if pb.SignalCount > 0 || pb.SupplierCount > 0 {
				doc.Hits = append(doc.Hits, models.DataCaptureHit{
					HitID:   "partsbase-summary-1",
					HitType: "partsbase_summary",
					Source:  firstNonEmpty(pb.Source, "PARTSBASE"),
					Identifiers: models.DataCaptureIdentifiers{
						NSN:  nsn,
						NIIN: niin,
						FSC:  fsc,
					},
					Context: strings.TrimSpace(pb.Note),
					Attributes: map[string]any{
						"signal_count":   pb.SignalCount,
						"exported_count": len(pb.Sample),
						"truncated":     len(pb.Sample) < pb.SignalCount,
						"supplier_count": pb.SupplierCount,
						"last_updated":   pb.LastUpdated,
						"source":         "insight_sample_fallback",
					},
				})
			}
			for i, row := range pb.Sample {
				up, ok := parseSingleUnitPrice(row.UnitPrice)
				if !ok {
					if v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(strings.ReplaceAll(row.UnitPrice, ",", ""), "$")), 64); err == nil && v > 0 {
						up, ok = v, true
					}
				}
				if !ok {
					continue
				}
				qty := row.Quantity
				if qty <= 0 {
					qty = 1
				}
				priceSeq++
				doc.Hits = append(doc.Hits, models.DataCaptureHit{
					HitID:   fmt.Sprintf("price-obs-%d", priceSeq),
					HitType: "price_observation",
					Source:  firstNonEmpty(pb.Source, "PARTSBASE"),
					Identifiers: models.DataCaptureIdentifiers{
						NSN:          nsn,
						NIIN:         niin,
						FSC:          fsc,
						Manufacturer: strings.TrimSpace(row.Supplier),
						Contract:     strings.TrimSpace(row.ContractNumber),
					},
					Pricing: &models.DataCapturePricing{
						UnitPrice:   roundMoney(up),
						Quantity:    qty,
						Currency:    "USD",
						Channel:     "partsbase",
						Merchant:    strings.TrimSpace(row.Supplier),
						PriceSource: "PARTSBASE",
						AsOf:        row.AwardDate,
					},
					Context: strings.TrimSpace(row.Context),
					Attributes: map[string]any{
						"condition_code": row.ConditionCode,
						"award_date":     row.AwardDate,
						"sample_index":   i + 1,
					},
				})
			}
		}
	}

	// 5) Related NSNs
	for i, rel := range result.RelatedNSNs {
		doc.Hits = append(doc.Hits, models.DataCaptureHit{
			HitID:   fmt.Sprintf("related-nsn-%d", i+1),
			HitType: "related_nsn",
			Source:  "SYNTHESIS",
			Identifiers: models.DataCaptureIdentifiers{
				NSN:        nsn,
				RelatedNSN: strings.TrimSpace(rel.NSN),
			},
			Description: strings.TrimSpace(rel.Description),
			Attributes: map[string]any{
				"relation":   rel.Relation,
				"confidence": rel.Confidence,
			},
		})
	}

	// 6) Federal award suppliers (no prices)
	for i, s := range result.SupplierData.TopSuppliers {
		doc.Hits = append(doc.Hits, models.DataCaptureHit{
			HitID:   fmt.Sprintf("federal-supplier-%d", i+1),
			HitType: "federal_supplier",
			Source:  "FPDS",
			Identifiers: models.DataCaptureIdentifiers{
				NSN:          nsn,
				CAGE:         strings.TrimSpace(s.CAGE),
				Manufacturer: strings.TrimSpace(s.Name),
			},
			Attributes: map[string]any{
				"award_count":       s.AwardCount,
				"total_value":       s.TotalValue,
				"country":           s.Country,
				"share_percent":     s.SharePercent,
				"most_recent_award": s.MostRecentAward,
			},
		})
	}

	// 7) Commercial supplier rollup (identity only — no example/range prices)
	for i, s := range result.TopCommercialSuppliers {
		doc.Hits = append(doc.Hits, models.DataCaptureHit{
			HitID:   fmt.Sprintf("commercial-supplier-%d", i+1),
			HitType: "commercial_supplier",
			Source:  firstNonEmpty(s.Source, "COMMERCIAL"),
			Identifiers: models.DataCaptureIdentifiers{
				NSN:          nsn,
				Manufacturer: strings.TrimSpace(s.Name),
			},
			Attributes: map[string]any{
				"reference_count": s.Count,
				"skus":            s.SKUs,
				"upcs":            s.UPCs,
				"priced_count":    s.PricedCount,
			},
		})
	}

	// 8) Web search results
	doc.Hits = append(doc.Hits, webSearchHitsFromSnapshots(snaps, nsn, niin, fsc)...)

	doc.Scores = &models.DataCaptureScores{
		SourcingAttractiveness: result.SourcingAttractiveness,
		SupplyRisk:             result.SupplyRisk,
		ViabilityScore:         result.ViabilityScore,
		RiskScore:              result.RiskScore,
		GeneratedAt:            result.GeneratedAt,
	}

	doc.Counts = computeDataCaptureCounts(doc.Hits)
	return doc
}

func commercialHitType(source string) string {
	switch strings.ToUpper(strings.TrimSpace(source)) {
	case "ABILITYONE_ETS":
		return "ets_mapping"
	case "GSA_ADVANTAGE":
		return "gsa_listing"
	case "ABILITYONE_COMMERCE":
		return "abilityone_commerce"
	case "PARTSBASE":
		return "partsbase_commercial"
	default:
		return "commercial_reference"
	}
}

func hasAnyLink(c models.CommercialReference) bool {
	return c.LinkShop != "" || c.LinkAmazon != "" || c.LinkUPC != "" || c.LinkGSA != "" || c.LinkWebsite != "" || c.PriceURL != ""
}

// singleEvidenceLink builds the schema 1.2 single-URL links object (or nil if empty).
// When kind is empty, it is derived from the URL. Explicit kinds (e.g. federal, web) are kept.
func singleEvidenceLink(rawURL, kind string) *models.DataCaptureLinks {
	rawURL = cleanEvidenceURL(rawURL)
	if rawURL == "" {
		return nil
	}
	if kind == "" {
		kind = classifyEvidenceURLKind(rawURL)
	}
	return &models.DataCaptureLinks{URL: rawURL, URLKind: kind}
}

// cleanEvidenceURL normalizes evidence URLs for export (fix broken query strings, strip tracking).
func cleanEvidenceURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Fix "…/203150945&intsrc=…" → "…/203150945?intsrc=…"
	if amp := strings.Index(raw, "&"); amp > 0 && !strings.Contains(raw[:amp], "?") {
		if strings.Contains(raw[amp:], "=") {
			raw = raw[:amp] + "?" + raw[amp+1:]
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	q := u.Query()
	for _, k := range []string{
		"srsltid", "gclid", "gbraid", "wbraid", "fbclid",
		"wmlspartner", "selectedSellerId", "veh", "cn",
		"source", "locale", "intsrc", "g_store", "afid", "cpng", "tcid",
		"utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term",
		"gucid", "cid", "scid", "ref_", "psc",
	} {
		q.Del(k)
		// Case variants
		for existing := range q {
			if strings.EqualFold(existing, k) {
				q.Del(existing)
			}
		}
	}
	u.RawQuery = q.Encode()
	// Drop trailing ? with no params
	out := u.String()
	if strings.HasSuffix(out, "?") {
		out = strings.TrimSuffix(out, "?")
	}
	return out
}

// classifyEvidenceURLKind maps a URL to a consumer-facing url_kind.
func classifyEvidenceURLKind(rawURL string) string {
	u := strings.ToLower(strings.TrimSpace(rawURL))
	if u == "" {
		return ""
	}
	if isSearchOrHubURL(u) {
		return "search"
	}
	if strings.Contains(u, "amazon.com/dp/") || strings.Contains(u, "amazon.com/gp/product/") {
		return "amazon_dp"
	}
	if strings.Contains(u, "abilityone.com") {
		return "federal"
	}
	if isMerchantProductURL(u) || isDirectProductURL(u) {
		return "merchant_pdp"
	}
	return "other"
}

// evidenceURLHost returns the lowercase hostname without www.
func evidenceURLHost(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		// Fallback: crude extract
		s := strings.ToLower(rawURL)
		s = strings.TrimPrefix(s, "https://")
		s = strings.TrimPrefix(s, "http://")
		if i := strings.IndexAny(s, "/?#"); i >= 0 {
			s = s[:i]
		}
		return strings.TrimPrefix(s, "www.")
	}
	return strings.TrimPrefix(strings.ToLower(u.Host), "www.")
}

// hostMatchesMerchant is true when the URL host is plausibly the named merchant.
// Critical for pricing evidence: never attach Home Depot URL to a Newegg price.
func hostMatchesMerchant(rawURL, merchant string) bool {
	host := evidenceURLHost(rawURL)
	if host == "" {
		return false
	}
	m := strings.ToLower(strings.TrimSpace(merchant))
	if m == "" {
		return true // unknown merchant — allow
	}
	// Normalize common retailer names → host tokens.
	type alias struct {
		needles []string
		hosts   []string
	}
	aliases := []alias{
		{[]string{"home depot", "homedepot"}, []string{"homedepot.com"}},
		{[]string{"walmart", "wal-mart"}, []string{"walmart.com"}},
		{[]string{"amazon"}, []string{"amazon.com", "amazon."}},
		{[]string{"lowe", "lowes"}, []string{"lowes.com"}},
		{[]string{"ace hardware", "acehardware"}, []string{"acehardware.com"}},
		{[]string{"true value", "truevalue"}, []string{"truevalue.com"}},
		{[]string{"do it best", "doitbest"}, []string{"doitbest.com"}},
		{[]string{"tractor supply"}, []string{"tractorsupply.com"}},
		{[]string{"office depot", "officedepot"}, []string{"officedepot.com"}},
		{[]string{"staples"}, []string{"staples.com"}},
		{[]string{"target"}, []string{"target.com"}},
		{[]string{"best buy", "bestbuy"}, []string{"bestbuy.com"}},
		{[]string{"grainger"}, []string{"grainger.com"}},
		{[]string{"zoro"}, []string{"zoro.com"}},
		{[]string{"menards"}, []string{"menards.com"}},
		{[]string{"newegg"}, []string{"newegg.com", "neweggbusiness.com"}},
		{[]string{"ebay"}, []string{"ebay.com"}},
		{[]string{"wayfair"}, []string{"wayfair.com"}},
		{[]string{"hd supply", "hdsupply"}, []string{"hdsupplysolutions.com", "hdsupply.com"}},
		{[]string{"abilityone"}, []string{"abilityone.com"}},
		{[]string{"painters solutions", "painterssolutions"}, []string{"painterssolutions.com"}},
		{[]string{"mccoy"}, []string{"mccoys.com"}},
		{[]string{"dk hardware", "dkhardware"}, []string{"dkhardware.com"}},
	}
	for _, a := range aliases {
		matchMerch := false
		for _, n := range a.needles {
			if strings.Contains(m, n) {
				matchMerch = true
				break
			}
		}
		if !matchMerch {
			continue
		}
		for _, h := range a.hosts {
			if strings.Contains(host, h) || strings.HasSuffix(host, h) {
				return true
			}
		}
		return false // known merchant but wrong host
	}
	// Generic: merchant tokens appear in host (e.g. "upsidedownsupply.com" vs "Up Side Down Supply").
	compactHost := strings.ReplaceAll(host, ".", "")
	compactHost = strings.ReplaceAll(compactHost, "-", "")
	words := strings.FieldsFunc(m, func(r rune) bool {
		return r == ' ' || r == '-' || r == '_' || r == '.' || r == ',' || r == '(' || r == ')'
	})
	var significant []string
	for _, w := range words {
		w = strings.ToLower(w)
		if len(w) < 4 {
			continue
		}
		// Skip generic tokens.
		switch w {
		case "shop", "store", "online", "inc", "llc", "com", "www", "the", "and", "supply", "supplies":
			continue
		}
		significant = append(significant, w)
	}
	if len(significant) == 0 {
		// Can't verify — for pricing evidence require a real product path at least.
		return isMerchantProductURL(rawURL) || isDirectProductURL(rawURL)
	}
	for _, w := range significant {
		if strings.Contains(compactHost, w) || strings.Contains(host, w) {
			return true
		}
	}
	return false
}

// isStrongPricingEvidenceURL is true for product detail pages suitable as price proof.
func isStrongPricingEvidenceURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || isSearchOrHubURL(rawURL) || isPermanentlyDeadHost(rawURL) {
		return false
	}
	kind := classifyEvidenceURLKind(rawURL)
	return kind == "merchant_pdp" || kind == "amazon_dp" || kind == "federal"
}

// bestCommercialEvidenceLinks picks one primary URL for an identity / mapping hit.
// Prefers brand-matched merchant PDPs; will use search only if nothing better exists.
func bestCommercialEvidenceLinks(c models.CommercialReference) *models.DataCaptureLinks {
	type cand struct {
		url  string
		kind string
	}
	// Also consider strong links from market offers (often better than parent LinkShop).
	cands := []cand{
		{c.PriceURL, ""},
		{c.LinkShop, ""},
		{c.LinkAmazon, ""},
		{c.LinkGSA, "federal"},
	}
	for _, o := range c.MarketOffers {
		if strings.TrimSpace(o.Link) != "" {
			cands = append(cands, cand{o.Link, ""})
		}
	}
	// Weak last resorts only if no strong evidence found.
	weak := []cand{
		{c.LinkUPC, "other"},
		{c.LinkWebsite, "other"},
	}

	scoreCand := func(u, forcedKind string) (kind string, sc int, ok bool) {
		u = cleanEvidenceURL(u)
		if u == "" || isPermanentlyDeadHost(u) {
			return "", 0, false
		}
		kind = forcedKind
		if kind == "" {
			kind = classifyEvidenceURLKind(u)
		}
		// Brand conflict → reject for identity evidence.
		if identityMatchScore(c.SKU, c.Manufacturer, c.Description, u) < 0 {
			return "", 0, false
		}
		sc = productURLQuality(u)
		switch kind {
		case "merchant_pdp":
			sc += 40
		case "amazon_dp":
			sc += 38
		case "federal":
			sc += 8
		case "search":
			sc -= 40 // avoid search for pricing evidence when possible
		case "other":
			sc -= 10
		}
		// UPC dossier is multi-merchant, not a listing price page — never preferred evidence.
		if strings.Contains(strings.ToLower(u), "upcitemdb.com") {
			sc -= 80
		}
		if c.LinkShopMerchant != "" && hostMatchesMerchant(u, c.LinkShopMerchant) {
			sc += 8
		}
		if isStrongPricingEvidenceURL(u) {
			sc += 15
		}
		// Bonus when SKU appears in URL path (stronger product identity).
		sku := strings.ToUpper(strings.TrimSpace(c.SKU))
		if sku != "" && len(sku) >= 3 && strings.Contains(strings.ToUpper(u), sku) {
			sc += 20
		}
		return kind, sc, true
	}

	bestScore := -1 << 30
	bestURL, bestKind := "", ""
	for _, cnd := range cands {
		kind, sc, ok := scoreCand(cnd.url, cnd.kind)
		if !ok {
			continue
		}
		if sc > bestScore {
			bestScore, bestURL, bestKind = sc, cleanEvidenceURL(cnd.url), kind
		}
	}
	// Weak candidates only if we still lack a real product page.
	if bestURL == "" || !isStrongPricingEvidenceURL(bestURL) {
		strongScore := bestScore
		for _, cnd := range weak {
			kind, sc, ok := scoreCand(cnd.url, cnd.kind)
			if !ok {
				continue
			}
			// Never let upcitemdb beat a real PDP we already have.
			if isStrongPricingEvidenceURL(bestURL) && strings.Contains(strings.ToLower(cnd.url), "upcitemdb.com") {
				continue
			}
			if bestURL == "" || sc > bestScore {
				bestScore, bestURL, bestKind = sc, cleanEvidenceURL(cnd.url), kind
			}
		}
		_ = strongScore
	}
	// Last resort: tight Google Shopping search (honest kind=search) — better than upcitemdb dossier.
	if bestURL == "" || strings.Contains(strings.ToLower(bestURL), "upcitemdb.com") || !isStrongPricingEvidenceURL(bestURL) && bestKind == "other" {
		if q := buildTightProductSearchQuery(c.Manufacturer, c.SKU, c.UPC, c.Description); q != "" {
			searchURL := "https://www.google.com/search?tbm=shop&q=" + url.QueryEscape(q)
			// Only replace weak/other; keep merchant_pdp / amazon_dp.
			if bestURL == "" || bestKind == "other" || strings.Contains(strings.ToLower(bestURL), "upcitemdb.com") {
				bestURL, bestKind = searchURL, "search"
			}
		}
	}
	if bestURL == "" {
		return nil
	}
	return singleEvidenceLink(bestURL, bestKind)
}

// bestPriceObservationLinks picks one URL for an atomic price hit used as pricing evidence.
// Rule: the URL must belong to the offer's merchant (or be omitted). Never attach a
// different retailer's product page to a price (e.g. HD URL on a Newegg observation).
func bestPriceObservationLinks(o models.MarketOffer, parent models.CommercialReference) *models.DataCaptureLinks {
	merchant := strings.TrimSpace(o.Merchant)
	ch := strings.ToLower(strings.TrimSpace(o.Channel))

	// Collect candidate URLs that are honest for this merchant/channel.
	try := func(raw string) *models.DataCaptureLinks {
		raw = cleanEvidenceURL(raw)
		if raw == "" || isPermanentlyDeadHost(raw) {
			return nil
		}
		// Search hubs are weak evidence for a specific unit_price.
		if isSearchOrHubURL(raw) {
			return nil
		}
		// Merchant must match when we know the merchant (pricing integrity).
		if merchant != "" && !hostMatchesMerchant(raw, merchant) {
			// Amazon channel exception: merchant text may be "Amazon Marketplace Used".
			if ch == "amazon" || strings.Contains(strings.ToLower(merchant), "amazon") {
				if classifyEvidenceURLKind(raw) == "amazon_dp" {
					return singleEvidenceLink(raw, "amazon_dp")
				}
			}
			return nil
		}
		if !isStrongPricingEvidenceURL(raw) {
			// Allow other product-ish paths that match merchant host.
			if !(isMerchantProductURL(raw) || isDirectProductURL(raw) || productURLQuality(raw) >= 40) {
				return nil
			}
		}
		return singleEvidenceLink(raw, classifyEvidenceURLKind(raw))
	}

	// 1) Offer's own link (best).
	if lnk := try(o.Link); lnk != nil {
		return lnk
	}

	// 2) Sibling market offers with the same merchant that still have a good link.
	merchKey := strings.ToLower(merchant)
	for _, sib := range parent.MarketOffers {
		if merchKey != "" && strings.ToLower(strings.TrimSpace(sib.Merchant)) != merchKey {
			// Fuzzy: same host family (e.g. "Walmart - Supply the Home" vs "Walmart")
			if !merchantsLooselyEqual(merchant, sib.Merchant) {
				continue
			}
		}
		if lnk := try(sib.Link); lnk != nil {
			return lnk
		}
	}

	// 3) Parent channel links only when host matches this merchant.
	var parentCands []string
	switch {
	case ch == "amazon" || strings.Contains(strings.ToLower(merchant), "amazon"):
		parentCands = []string{parent.LinkAmazon, parent.PriceURL}
	case ch == "federal":
		parentCands = []string{parent.LinkGSA, parent.PriceURL}
	default:
		parentCands = []string{parent.LinkShop, parent.PriceURL, parent.LinkAmazon}
	}
	for _, p := range parentCands {
		if lnk := try(p); lnk != nil {
			return lnk
		}
	}

	// 4) No honest URL — omit rather than misattribute another retailer's page.
	return nil
}

// merchantsLooselyEqual treats "Walmart - Supply the Home" ≈ "Wal-Mart.com".
func merchantsLooselyEqual(a, b string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	// Primary token (before dash/parenthesis).
	prim := func(s string) string {
		if i := strings.IndexAny(s, "-("); i > 0 {
			s = s[:i]
		}
		return strings.TrimSpace(s)
	}
	pa, pb := prim(a), prim(b)
	if pa != "" && pb != "" && (strings.Contains(pa, pb) || strings.Contains(pb, pa)) {
		return true
	}
	// Shared significant token length >= 5
	wa := strings.FieldsFunc(a, func(r rune) bool {
		return r == ' ' || r == '-' || r == '.' || r == ','
	})
	for _, w := range wa {
		if len(w) >= 5 && strings.Contains(b, w) {
			return true
		}
	}
	return false
}

func buildDataCaptureSources(snaps []models.DataSnapshot) []models.DataCaptureSource {
	if len(snaps) == 0 {
		return nil
	}
	out := make([]models.DataCaptureSource, 0, len(snaps))
	for _, s := range snaps {
		src := models.DataCaptureSource{
			SourceCode:   s.SourceCode,
			SnapshotID:   s.ID,
			SnapshotAt:   s.SnapshotAt,
			QualityScore: s.QualityScore,
		}
		if s.RawResponse != nil {
			if ds, ok := s.RawResponse["data_source"].(string); ok {
				src.DataSource = ds
			}
			if note, ok := s.RawResponse["note"].(string); ok {
				src.Note = note
			}
			src.ResultCount = intFromAny(s.RawResponse["result_count"])
			if src.ResultCount == 0 {
				src.ResultCount = intFromAny(s.RawResponse["matched_rows_count"])
			}
			if src.ResultCount == 0 {
				src.ResultCount = intFromAny(s.RawResponse["references_returned"])
			}
		}
		out = append(out, src)
	}
	return out
}

// partsBasePriceHitsFromSnapshots expands every usable price_signal from the
// live PARTSBASE snapshot into atomic price_observation hits (no cap).
func partsBasePriceHitsFromSnapshots(snaps []models.DataSnapshot, nsn, niin, fsc string, priceSeq *int) (hits []models.DataCaptureHit, total int) {
	pb, ok := findPartsBaseSnapshot(snaps)
	if !ok || pb.RawResponse == nil {
		return nil, 0
	}
	signals := mapSliceFromAny(pb.RawResponse["price_signals"])
	if len(signals) == 0 {
		return nil, 0
	}
	total = len(signals)
	for i, s := range signals {
		unit := toFloatFromAny(s["unit_price"])
		if unit <= 0 {
			unit = toFloatFromAny(s["price"])
		}
		if unit <= 0 {
			if p := firstNonEmptyString(s, "unit_price", "price"); p != "" {
				if up, ok := parseSingleUnitPrice(p); ok {
					unit = up
				} else {
					unit = toFloatFromAny(strings.TrimPrefix(strings.TrimSpace(p), "$"))
				}
			}
		}
		if unit <= 0 {
			continue
		}
		qty := intFromAny(s["quantity"])
		if qty <= 0 {
			qty = 1
		}
		supplier := firstNonEmptyString(s, "supplier", "vendor", "manufacturer")
		contract := firstNonEmptyString(s, "contract_number", "contractNo", "contract")
		awardDate := firstNonEmptyString(s, "award_date", "AwardDate")
		cond := firstNonEmptyString(s, "condition_code", "ConditionCode", "condition")
		ctx := firstNonEmptyString(s, "context")
		*priceSeq++
		hits = append(hits, models.DataCaptureHit{
			HitID:   fmt.Sprintf("price-obs-%d", *priceSeq),
			HitType: "price_observation",
			Source:  "PARTSBASE",
			Identifiers: models.DataCaptureIdentifiers{
				NSN:          nsn,
				NIIN:         niin,
				FSC:          fsc,
				Manufacturer: supplier,
				Contract:     contract,
			},
			Pricing: &models.DataCapturePricing{
				UnitPrice:   roundMoney(unit),
				Quantity:    qty,
				Currency:    "USD",
				Channel:     "partsbase",
				Merchant:    supplier,
				PriceSource: "PARTSBASE",
				AsOf:        awardDate,
			},
			Context: ctx,
			Attributes: map[string]any{
				"condition_code": cond,
				"award_date":     awardDate,
				"signal_index":   i + 1,
			},
		})
	}
	return hits, total
}

func webSearchHitsFromSnapshots(snaps []models.DataSnapshot, nsn, niin, fsc string) []models.DataCaptureHit {
	var hits []models.DataCaptureHit
	idx := 0
	for _, s := range snaps {
		if s.SourceCode != "WEB_SEARCH_INTEL" || s.RawResponse == nil {
			continue
		}
		rawResults, ok := s.RawResponse["results"]
		if !ok {
			continue
		}
		switch rows := rawResults.(type) {
		case []map[string]any:
			for _, row := range rows {
				idx++
				hits = append(hits, webHitFromMap(row, idx, nsn, niin, fsc))
			}
		case []any:
			for _, item := range rows {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				idx++
				hits = append(hits, webHitFromMap(m, idx, nsn, niin, fsc))
			}
		}
	}
	return hits
}

func webHitFromMap(row map[string]any, idx int, nsn, niin, fsc string) models.DataCaptureHit {
	title := strings.TrimSpace(fmt.Sprint(row["title"]))
	if title == "<nil>" {
		title = ""
	}
	url := strings.TrimSpace(fmt.Sprint(row["url"]))
	if url == "<nil>" {
		url = ""
	}
	domain := strings.TrimSpace(fmt.Sprint(row["domain"]))
	if domain == "<nil>" {
		domain = ""
	}
	snippet := strings.TrimSpace(fmt.Sprint(row["snippet"]))
	if snippet == "<nil>" {
		snippet = ""
	}
	attrs := map[string]any{}
	if domain != "" {
		attrs["domain"] = domain
	}
	return models.DataCaptureHit{
		HitID:   fmt.Sprintf("web-result-%d", idx),
		HitType: "web_result",
		Source:  "WEB_SEARCH_INTEL",
		Identifiers: models.DataCaptureIdentifiers{
			NSN:  nsn,
			NIIN: niin,
			FSC:  fsc,
		},
		Description: title,
		Context:     snippet,
		Links:      singleEvidenceLink(url, "web"),
		Attributes: attrs,
	}
}

func computeDataCaptureCounts(hits []models.DataCaptureHit) models.DataCaptureCounts {
	c := models.DataCaptureCounts{
		TotalHits: len(hits),
		ByType:    map[string]int{},
		BySource:  map[string]int{},
	}
	skus := map[string]struct{}{}
	upcs := map[string]struct{}{}
	mfrs := map[string]struct{}{}
	for _, h := range hits {
		c.ByType[h.HitType]++
		src := h.Source
		if src == "" {
			src = "UNKNOWN"
		}
		c.BySource[src]++
		if sku := strings.TrimSpace(h.Identifiers.SKU); sku != "" {
			skus[sku] = struct{}{}
		}
		if upc := digitsOnlyString(h.Identifiers.UPC); len(upc) >= 11 {
			upcs[upc] = struct{}{}
		}
		if m := strings.TrimSpace(h.Identifiers.Manufacturer); m != "" {
			mfrs[m] = struct{}{}
		}
		if h.Pricing != nil && h.Pricing.UnitPrice > 0 {
			c.PricedHits++
		}
		if h.HitType == "price_observation" {
			c.PriceObservations++
		}
	}
	c.UniqueSKUs = len(skus)
	c.UniqueUPCs = len(upcs)
	c.UniqueManufacturers = len(mfrs)
	return c
}

func formatDashedNSNLocal(nsn string) string {
	d := digitsOnlyString(nsn)
	if len(d) != 13 {
		return d
	}
	return d[:4] + "-" + d[4:6] + "-" + d[6:9] + "-" + d[9:]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func roundMoney(v float64) float64 {
	// Two decimal places without importing math for simple rounding.
	return float64(int(v*100+0.5)) / 100
}
