package processing

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
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
			"Each hit has at most one primary evidence URL (links.url). Not the pricing-tool narrative export.",
		ExportedAt:    time.Now().UTC(),
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
// When kind is empty, it is derived from the URL. Explicit kinds (e.g. federal, web) are kept
// even if the URL looks like a catalog search page.
func singleEvidenceLink(rawURL, kind string) *models.DataCaptureLinks {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	if kind == "" {
		kind = classifyEvidenceURLKind(rawURL)
	}
	return &models.DataCaptureLinks{URL: rawURL, URLKind: kind}
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

// bestCommercialEvidenceLinks picks one primary URL for an identity / mapping hit.
func bestCommercialEvidenceLinks(c models.CommercialReference) *models.DataCaptureLinks {
	type cand struct {
		url  string
		kind string
		// Higher is better.
		score int
	}
	// Prefer merchant PDPs and price-evidence pages over search hubs / manufacturer home.
	cands := []cand{
		{c.PriceURL, "", 0},
		{c.LinkShop, "", 0},
		{c.LinkAmazon, "", 0},
		{c.LinkGSA, "federal", 0},
		{c.LinkUPC, "other", 0},
		// Website homepage is weak product evidence — only if nothing else.
		{c.LinkWebsite, "other", 0},
	}
	bestScore := -1
	best := cand{}
	for _, cand := range cands {
		u := strings.TrimSpace(cand.url)
		if u == "" || isPermanentlyDeadHost(u) {
			continue
		}
		kind := cand.kind
		if kind == "" {
			kind = classifyEvidenceURLKind(u)
		}
		sc := productURLQuality(u)
		switch kind {
		case "merchant_pdp":
			sc += 30
		case "amazon_dp":
			sc += 28
		case "federal":
			sc += 10
		case "search":
			sc -= 20
		case "other":
			// UPC dossier / manufacturer site
			if strings.Contains(strings.ToLower(u), "upcitemdb.com/upc/") {
				sc += 5
			}
		}
		// Prefer channel that matches a known shop merchant when available.
		if c.LinkShopMerchant != "" && u == c.LinkShop {
			sc += 5
		}
		if sc > bestScore {
			bestScore = sc
			best = cand
			best.url = u
			best.kind = kind
		}
	}
	if best.url == "" {
		return nil
	}
	return singleEvidenceLink(best.url, best.kind)
}

// bestPriceObservationLinks picks one URL for an atomic price hit.
// Prefers the offer's own link when it is product-quality; else parent commercial best
// matching channel (amazon → amazon link, shop → shop link).
func bestPriceObservationLinks(o models.MarketOffer, parent models.CommercialReference) *models.DataCaptureLinks {
	offerLink := strings.TrimSpace(o.Link)
	if offerLink != "" && !isPermanentlyDeadHost(offerLink) {
		// Accept merchant PDPs and amazon dp; allow search only if nothing better later.
		q := productURLQuality(offerLink)
		if q >= 50 || isDirectProductURL(offerLink) || isMerchantProductURL(offerLink) {
			return singleEvidenceLink(offerLink, classifyEvidenceURLKind(offerLink))
		}
		// Weak offer link — still use if parent has nothing better for this channel.
		if isSearchOrHubURL(offerLink) {
			// fall through to parent selection, keep as backup
		} else if q > 0 {
			return singleEvidenceLink(offerLink, classifyEvidenceURLKind(offerLink))
		}
	}

	ch := strings.ToLower(strings.TrimSpace(o.Channel))
	var fallback string
	switch {
	case ch == "amazon" || strings.Contains(strings.ToLower(o.Merchant), "amazon"):
		fallback = firstNonEmpty(parent.LinkAmazon, parent.PriceURL, parent.LinkShop)
	case ch == "federal":
		fallback = firstNonEmpty(parent.LinkGSA, parent.PriceURL)
	default:
		fallback = firstNonEmpty(parent.LinkShop, parent.PriceURL, parent.LinkAmazon)
	}
	if fallback == "" {
		if offerLink != "" && !isPermanentlyDeadHost(offerLink) {
			return singleEvidenceLink(offerLink, classifyEvidenceURLKind(offerLink))
		}
		return bestCommercialEvidenceLinks(parent)
	}
	// Prefer offer link if both exist and offer is at least as good.
	if offerLink != "" && productURLQuality(offerLink) >= productURLQuality(fallback) {
		return singleEvidenceLink(offerLink, classifyEvidenceURLKind(offerLink))
	}
	return singleEvidenceLink(fallback, classifyEvidenceURLKind(fallback))
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
