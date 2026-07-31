package processing

import (
	"fmt"
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
		Purpose:       "Machine-readable inventory of NSN/SKU/UPC/ETS/commercial/procurement hits for downstream applications. Not the pricing-tool narrative export.",
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

	// 1) Item-master identity (one hit when we have a name/description)
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

	// 2) Commercial / ETS / GSA references — primary product hits
	for i, c := range result.CommercialReferences {
		hitType := commercialHitType(c.Source)
		hit := models.DataCaptureHit{
			HitID:   fmt.Sprintf("%s-%d", hitType, i+1),
			HitType: hitType,
			Source:  firstNonEmpty(c.Source, "COMMERCIAL"),
			Identifiers: models.DataCaptureIdentifiers{
				NSN:          nsn,
				NIIN:         niin,
				FSC:          fsc,
				SKU:          strings.TrimSpace(c.SKU),
				UPC:          digitsOnlyString(c.UPC),
				GTIN:         digitsOnlyString(c.GTIN),
				Manufacturer: strings.TrimSpace(c.Manufacturer),
			},
			Description: strings.TrimSpace(c.Description),
			Context:     strings.TrimSpace(c.Context),
			DateAdded:   strings.TrimSpace(c.DateAdded),
		}
		if hasAnyPrice(c) {
			hit.Pricing = &models.DataCapturePricing{
				Primary:       c.Price,
				PrimarySource: c.PriceSource,
				AsOf:          c.PriceAsOf,
				Amazon:        c.PriceAmazon,
				AmazonSource:  c.PriceAmazonSrc,
				AmazonIsRange: c.PriceAmazonIsRange,
				Shop:          c.PriceShop,
				ShopSource:    c.PriceShopSrc,
				ShopIsRange:   c.PriceShopIsRange,
				UPC:           c.PriceUPC,
				UPCSource:     c.PriceUPCSrc,
				UPCIsRange:    c.PriceUPCIsRange,
				Federal:       c.PriceFederal,
				FederalSource: c.PriceFederalSrc,
			}
		}
		if hasAnyLink(c) {
			hit.Links = &models.DataCaptureLinks{
				Shop:     c.LinkShop,
				Amazon:   c.LinkAmazon,
				UPC:      c.LinkUPC,
				Federal:  c.LinkGSA,
				Website:  c.LinkWebsite,
				PriceURL: c.PriceURL,
			}
		}
		doc.Hits = append(doc.Hits, hit)
	}

	// 3) AbilityOne.com NSN channel price
	if ao := result.AbilityOneChannelPrice; ao != nil && strings.TrimSpace(ao.Price) != "" {
		doc.Hits = append(doc.Hits, models.DataCaptureHit{
			HitID:   "channel-price-abilityone-1",
			HitType: "channel_price",
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
				Primary:       ao.Price,
				PrimarySource: firstNonEmpty(ao.Source, "ABILITYONE_COM"),
				AsOf:          ao.AsOf,
				Federal:       ao.Price,
				FederalSource: firstNonEmpty(ao.Source, "ABILITYONE_COM"),
			},
			Links: &models.DataCaptureLinks{
				Federal: ao.URL,
				URL:     ao.URL,
			},
			Context: strings.TrimSpace(ao.Note),
		})
	}

	// 4) PartsBase historical transaction samples + summary
	if pb := result.PartsBaseHistoricalPricing; pb != nil {
		if pb.SignalCount > 0 || pb.MinUnitPrice != "" || pb.MaxUnitPrice != "" {
			doc.Hits = append(doc.Hits, models.DataCaptureHit{
				HitID:   "partsbase-summary-1",
				HitType: "partsbase_summary",
				Source:  firstNonEmpty(pb.Source, "PARTSBASE"),
				Identifiers: models.DataCaptureIdentifiers{
					NSN:  nsn,
					NIIN: niin,
					FSC:  fsc,
				},
				Pricing: &models.DataCapturePricing{
					Min:    pb.MinUnitPrice,
					Max:    pb.MaxUnitPrice,
					Median: pb.MedianUnitPrice,
					AsOf:   pb.LastUpdated,
				},
				Context: strings.TrimSpace(pb.Note),
				Attributes: map[string]any{
					"signal_count":   pb.SignalCount,
					"supplier_count": pb.SupplierCount,
				},
			})
		}
		for i, row := range pb.Sample {
			doc.Hits = append(doc.Hits, models.DataCaptureHit{
				HitID:   fmt.Sprintf("partsbase-tx-%d", i+1),
				HitType: "partsbase_transaction",
				Source:  firstNonEmpty(pb.Source, "PARTSBASE"),
				Identifiers: models.DataCaptureIdentifiers{
					NSN:      nsn,
					NIIN:     niin,
					FSC:      fsc,
					Manufacturer: strings.TrimSpace(row.Supplier),
					Contract: strings.TrimSpace(row.ContractNumber),
				},
				Pricing: &models.DataCapturePricing{
					UnitPrice: row.UnitPrice,
					Primary:   row.UnitPrice,
					Quantity:  row.Quantity,
					AsOf:      row.AwardDate,
				},
				Context: strings.TrimSpace(row.Context),
				Attributes: map[string]any{
					"condition_code": row.ConditionCode,
					"award_date":     row.AwardDate,
					"supplier":       row.Supplier,
				},
			})
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

	// 6) Federal award suppliers (aggregated)
	for i, s := range result.SupplierData.TopSuppliers {
		doc.Hits = append(doc.Hits, models.DataCaptureHit{
			HitID:   fmt.Sprintf("federal-supplier-%d", i+1),
			HitType: "federal_supplier",
			Source:  "FPDS",
			Identifiers: models.DataCaptureIdentifiers{
				NSN:  nsn,
				CAGE: strings.TrimSpace(s.CAGE),
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

	// 7) Commercial supplier rollup (from SKU/UPC concentration)
	for i, s := range result.TopCommercialSuppliers {
		attrs := map[string]any{
			"reference_count": s.Count,
			"skus":            s.SKUs,
			"upcs":            s.UPCs,
		}
		if s.PricedCount > 0 {
			attrs["priced_count"] = s.PricedCount
		}
		hit := models.DataCaptureHit{
			HitID:   fmt.Sprintf("commercial-supplier-%d", i+1),
			HitType: "commercial_supplier",
			Source:  firstNonEmpty(s.Source, "COMMERCIAL"),
			Identifiers: models.DataCaptureIdentifiers{
				NSN:          nsn,
				Manufacturer: strings.TrimSpace(s.Name),
			},
			Attributes: attrs,
		}
		if s.ExamplePrice != "" {
			hit.Pricing = &models.DataCapturePricing{
				Primary:       s.ExamplePrice,
				PrimarySource: "MARKET_RANGE",
			}
		}
		doc.Hits = append(doc.Hits, hit)
	}

	// 8) Web search results from snapshots (not already commercial tiles)
	doc.Hits = append(doc.Hits, webSearchHitsFromSnapshots(snaps, nsn, niin, fsc)...)

	// 9) Optional scores context
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

func hasAnyPrice(c models.CommercialReference) bool {
	return c.Price != "" || c.PriceAmazon != "" || c.PriceShop != "" || c.PriceUPC != "" || c.PriceFederal != ""
}

func hasAnyLink(c models.CommercialReference) bool {
	return c.LinkShop != "" || c.LinkAmazon != "" || c.LinkUPC != "" || c.LinkGSA != "" || c.LinkWebsite != "" || c.PriceURL != ""
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
		// results may be []map[string]any or []any
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
		Links: &models.DataCaptureLinks{
			URL: url,
		},
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
		if hitHasPrice(h) {
			c.PricedHits++
		}
	}
	c.UniqueSKUs = len(skus)
	c.UniqueUPCs = len(upcs)
	c.UniqueManufacturers = len(mfrs)
	return c
}

func hitHasPrice(h models.DataCaptureHit) bool {
	if h.Pricing == nil {
		return false
	}
	p := h.Pricing
	return p.Primary != "" || p.Amazon != "" || p.Shop != "" || p.UPC != "" ||
		p.Federal != "" || p.UnitPrice != "" || p.Min != "" || p.Max != "" || p.Median != ""
}

func formatDashedNSNLocal(nsn string) string {
	d := digitsOnlyString(nsn)
	if len(d) != 13 {
		return d
	}
	// FSC-NIIN group: 1234-00-123-4567
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
