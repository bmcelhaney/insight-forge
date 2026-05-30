package extraction

import (
	"context"
	"math/rand"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// WebFLISExtractor queries public WebFLIS / PUB LOG data for NSN characteristics,
// pricing history, packaging, unit of issue, etc.
// Prototype uses rich deterministic mocks. Real version would integrate with
// official DLA WebFLIS or authorized data services.
type WebFLISExtractor struct{}

func NewWebFLISExtractor() *WebFLISExtractor {
	return &WebFLISExtractor{}
}

func (w *WebFLISExtractor) SourceCode() string { return "WEBFLIS" }

func (w *WebFLISExtractor) Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error) {
	select {
	case <-time.After(220 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	seed := hashToInt(entityID + "webflis")
	r := rand.New(rand.NewSource(seed))
	now := time.Now()

	fsc := "0000"
	if len(entityID) >= 4 {
		fsc = entityID[:4]
	}

	// Category-aware item identity so different NSNs feel like real, distinct federal items
	itemName, unitOfIssue, techChars, basePrice := deriveWebFLISItem(fsc, entityID, r)

	snap := models.DataSnapshot{
		EntityID:   entityID,
		SourceCode: "WEBFLIS",
		SnapshotAt: now.Add(-time.Duration(r.Intn(180)) * 24 * time.Hour),
		RawResponse: map[string]any{
			"niin":                      entityID,
			"fsc":                       fsc,
			"item_name":                 itemName,
			"unit_of_issue":             unitOfIssue,
			"unit_price":                basePrice + r.Intn(1200),
			"packaging":                 "MIL-STD-2073",
			"technical_characteristics": techChars,
			"last_updated":              now.Add(-time.Duration(r.Intn(90)) * 24 * time.Hour).Format("2006-01-02"),
			"acquisition_advice_code":   []string{"D", "J", "Z"}[r.Intn(3)],
			"controlled_inventory":      r.Intn(10) > 7,
			"note":                      "Prototype WebFLIS data — expanded fields for deeper dynamic synthesis",
		},
		QualityScore: 0.88 + r.Float64()*0.09,
		CreatedBy:    "webflis-extractor-v1.2",
	}

	return []models.DataSnapshot{snap}, nil
}

// deriveWebFLISItem returns plausible federal item master data based on FSC prefix.
// This makes every NSN feel like a real, distinct catalog item instead of canned generic data.
func deriveWebFLISItem(fsc, entityID string, r *rand.Rand) (itemName, unitOfIssue, techChars string, basePrice int) {
	switch fsc {
	case "7920": // Cleaning supplies, towels, mops, sponges (AbilityOne heavy)
		itemName = "TOWEL, PAPER, CLEANING, HEAVY DUTY"
		if r.Intn(3) == 0 {
			itemName = "CLOTH, CLEANING, NONWOVEN, INDUSTRIAL"
		}
		unitOfIssue = "BX"
		techChars = "12x12 in sheets, 150 per box, high absorbency, low lint, suitable for solvents and water"
		basePrice = 18 + r.Intn(9)
	case "7520": // Office supplies, pens, pencils (AbilityOne classic)
		itemName = "PEN, BALL-POINT, BLACK, MEDIUM POINT"
		if r.Intn(4) == 0 {
			itemName = "PENCIL, MECHANICAL, .5MM"
		}
		unitOfIssue = "DZ"
		techChars = "Retractable, 1.0mm medium point, black ink, 12 per pack, GSA approved"
		basePrice = 6 + r.Intn(5)
	case "8105": // Bags and sacks
		itemName = "BAG, PLASTIC, RECLOSABLE, 10X12 IN"
		unitOfIssue = "BX"
		techChars = "2 mil thickness, zipper closure, 1000 per case, food-contact safe variant available"
		basePrice = 42 + r.Intn(18)
	case "7125": // Shelving, lockers, storage
		itemName = "SHELF, METAL, STORAGE, 36X18 IN"
		unitOfIssue = "EA"
		techChars = "18 ga steel, 300 lb capacity per shelf, powder coat finish, boltless assembly"
		basePrice = 68 + r.Intn(35)
	case "5180": // Tool kits and sets
		itemName = "TOOL KIT, GENERAL MECHANIC'S, 60 PIECE"
		unitOfIssue = "KT"
		techChars = "Includes sockets, wrenches, pliers, drivers in blow-mold case, commercial + custom components"
		basePrice = 185 + r.Intn(90)
	default:
		// Generic but still varied federal hardware/consumable
		itemNames := []string{
			"BRACKET, ANGLE, STEEL", "SEAL, O-RING, SYNTHETIC RUBBER",
			"WASHER, FLAT, STAINLESS", "CLAMP, HOSE, WORM DRIVE",
			"CONNECTOR, ELECTRICAL, CIRCULAR",
		}
		itemName = itemNames[r.Intn(len(itemNames))]
		unitOfIssue = "EA"
		techChars = "Federal stock item, MIL-spec compliant where applicable, current revision"
		basePrice = 12 + r.Intn(85)
	}
	return itemName, unitOfIssue, techChars, basePrice
}
