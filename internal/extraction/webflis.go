package extraction

import (
	"context"
	"fmt"
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

	// === Expanded coverage for common federal / facilities / maintenance FSCs ===
	case "7220": // Floor coverings (rugs, mats, carpet, runners — AbilityOne relevant)
		itemNames := []string{
			"MAT, FLOOR, RUBBER, 3X5 FT",
			"RUG, SCATTER, NYLON, 4X6 FT",
			"RUNNER, FLOOR, VINYL, 36 IN WIDE",
			"MAT, ENTRANCE, CARPET, 4X6 FT",
			"Tile, FLOOR, VINYL, 12X12 IN",
		}
		itemName = itemNames[r.Intn(len(itemNames))]
		unitOfIssue = "EA"
		techChars = "Commercial grade floor covering, slip resistant, suitable for high traffic federal facilities"
		basePrice = 35 + r.Intn(85)

	case "7210": // Household furnishings (bedding, towels, linens)
		itemName = "BLANKET, BED, WOOL, 66X90 IN"
		if r.Intn(3) == 0 {
			itemName = "SHEET, BED, COTTON, 54X90 IN"
		}
		unitOfIssue = "EA"
		techChars = "Institutional grade bedding/linens for barracks, hospitals, or quarters"
		basePrice = 22 + r.Intn(28)

	case "7230": // Draperies, awnings, shades
		itemName = "SHADE, WINDOW, ROLLER, 36X72 IN"
		unitOfIssue = "EA"
		techChars = "Light filtering or room darkening window treatment for federal buildings"
		basePrice = 18 + r.Intn(22)

	case "7240": // Household and commercial utility containers
		itemName = "CAN, TRASH, PLASTIC, 32 GAL"
		unitOfIssue = "EA"
		techChars = "Heavy duty waste container with lid, for indoor/outdoor federal facility use"
		basePrice = 28 + r.Intn(25)

	case "7310": // Food preparation and serving equipment
		itemName = "OVEN, MICROWAVE, COMMERCIAL, 1000W"
		unitOfIssue = "EA"
		techChars = "Heavy duty food service equipment for galleys, cafeterias, or break rooms"
		basePrice = 420 + r.Intn(180)

	case "7320": // Kitchen equipment and appliances
		itemName = "DISHWASHER, COMMERCIAL, UNDERCOUNTER"
		unitOfIssue = "EA"
		techChars = "High temperature commercial dishwasher for institutional food service"
		basePrice = 1850 + r.Intn(650)

	case "7330": // Kitchen hand tools and utensils
		itemName = "KNIFE, BUTCHER, 10 IN BLADE"
		unitOfIssue = "EA"
		techChars = "Heavy duty stainless kitchen utensil for institutional use"
		basePrice = 14 + r.Intn(12)

	case "7350": // Tableware (dishes, flatware, glassware)
		itemName = "PLATE, DINNER, MELAMINE, 10 IN"
		unitOfIssue = "DZ"
		techChars = "Break resistant institutional tableware for dining facilities"
		basePrice = 18 + r.Intn(15)

	case "7610": // Books and pamphlets
		itemName = "MANUAL, TECHNICAL, EQUIPMENT MAINTENANCE"
		unitOfIssue = "EA"
		techChars = "Official technical publication for federal equipment or systems"
		basePrice = 32 + r.Intn(28)

	case "7690": // Miscellaneous printed matter (decals, labels, signs)
		itemName = "DECAL, INSTRUCTIONAL, 6X4 IN"
		unitOfIssue = "PG"
		techChars = "Pressure sensitive instructional or safety signage for federal equipment/facilities"
		basePrice = 9 + r.Intn(11)

	case "7910": // Floor polishers, vacuum cleaners, carpet shampooers
		itemName = "VACUUM CLEANER, UPRIGHT, COMMERCIAL"
		unitOfIssue = "EA"
		techChars = "Heavy duty commercial floor care equipment for large facilities"
		basePrice = 285 + r.Intn(95)

	case "7930": // Cleaning and polishing compounds and preparations
		itemName = "CLEANER, GENERAL PURPOSE, LIQUID, 1 GAL"
		unitOfIssue = "BX"
		techChars = "Concentrated institutional cleaning compound, NSN common for janitorial contracts"
		basePrice = 12 + r.Intn(14)

	case "8010": // Paints, dopes, varnishes, and related products
		itemName = "PAINT, LATEX, INTERIOR, WHITE, 1 GAL"
		unitOfIssue = "CN"
		techChars = "Low VOC institutional paint for federal building maintenance"
		basePrice = 38 + r.Intn(22)

	case "8030": // Preservative and sealing compounds
		itemName = "COMPOUND, CORROSION PREVENTIVE, 1 QT"
		unitOfIssue = "CN"
		techChars = "Protective coating for metal parts and equipment in storage or transit"
		basePrice = 26 + r.Intn(18)

	case "8040": // Adhesives
		itemName = "ADHESIVE, EPOXY, 2 PART, 1 QT KIT"
		unitOfIssue = "KT"
		techChars = "Structural adhesive for general maintenance and repair"
		basePrice = 32 + r.Intn(24)

	case "8540": // Toiletry paper products (very common AbilityOne)
		itemNames := []string{
			"TOILET PAPER, ROLL, 2 PLY, 500 SHEETS",
			"PAPER TOWEL, ROLL, HARDWOUND, 8 IN",
			"FACIAL TISSUE, BOX, 2 PLY, 100 COUNT",
		}
		itemName = itemNames[r.Intn(len(itemNames))]
		unitOfIssue = "BX"
		techChars = "Standard commercial grade paper product for federal restrooms and facilities"
		basePrice = 24 + r.Intn(18)

	case "8415": // Clothing, special purpose (work gloves, protective garments, etc. — many AbilityOne items)
		itemName = "GLOVES, WORK, IMPACT PROTECTION"
		if r.Intn(4) == 0 {
			itemName = "GLOVES, ANTI-STATIC, TACTICAL"
		}
		unitOfIssue = "PR"
		techChars = "Special purpose work or tactical gloves with impact protection, dexterity features, and often anti-static or touchscreen compatibility for technical/industrial/military use"
		basePrice = 18 + r.Intn(25)

	// === Hardware / construction fallback range ===
	case "5305", "5310", "5320", "5340":
		itemName = "SCREW, MACHINE, STEEL, 1/4-20 X 1 IN"
		if fsc == "5310" {
			itemName = "NUT, HEX, STEEL, 1/4-20"
		} else if fsc == "5320" {
			itemName = "RIVET, BLIND, 1/8 X 1/4 IN"
		} else if fsc == "5340" {
			itemName = "BRACKET, ANGLE, STEEL, 2X2X1/8 IN"
		}
		unitOfIssue = "PG"
		techChars = "General hardware item for maintenance and construction"
		basePrice = 8 + r.Intn(15)

	default:
		// Much smarter broad-category fallback based on FSC range
		itemName, techChars, basePrice = getBroadCategoryForFSC(fsc, r)
		unitOfIssue = "EA"
	}
	return itemName, unitOfIssue, techChars, basePrice
}

// getBroadCategoryForFSC gives a reasonable category name when we don't have a specific mapping.
// This prevents the previous problem of returning completely wrong specific names (like electrical connector for paper).
func getBroadCategoryForFSC(fsc string, r *rand.Rand) (name, tech string, price int) {
	fscNum := 0
	fmt.Sscanf(fsc, "%d", &fscNum)

	switch {
	case fscNum >= 1000 && fscNum < 2000:
		name = "AIRCRAFT OR WEAPONS SYSTEM COMPONENT"
		tech = "Aviation or ordnance related part. Detailed master data limited in this prototype."
		price = 120 + r.Intn(480)
	case fscNum >= 2000 && fscNum < 3000:
		name = "VEHICLE OR ENGINE COMPONENT"
		tech = "Ground vehicle or engine part. Prototype data does not include full technical characteristics."
		price = 85 + r.Intn(320)
	case fscNum >= 5300 && fscNum < 5500:
		name = "HARDWARE AND FASTENER"
		tech = "General mechanical hardware item for maintenance and repair."
		price = 6 + r.Intn(28)
	case fscNum >= 7100 && fscNum < 7300:
		name = "FURNITURE OR FURNISHINGS ITEM"
		tech = "Office or institutional furniture/furnishings. Prototype data is summarized."
		price = 95 + r.Intn(280)
	case fscNum >= 7300 && fscNum < 7600:
		name = "FOOD SERVICE OR KITCHEN EQUIPMENT"
		tech = "Institutional food service or kitchen item."
		price = 180 + r.Intn(650)
	case fscNum >= 7900 && fscNum < 8600:
		name = "CLEANING, PACKAGING OR PERSONAL CARE ITEM"
		tech = "Janitorial, packaging or personal care consumable/supply."
		price = 14 + r.Intn(35)
	case fscNum >= 8900 && fscNum < 9100:
		name = "SUBSISTENCE OR FOOD ITEM"
		tech = "Food or subsistence product for federal feeding operations."
		price = 12 + r.Intn(45)
	default:
		name = "FEDERAL STOCK ITEM (FSC " + fsc + ")"
		tech = "Item identity and characteristics limited in current prototype data. Real WebFLIS record would contain precise nomenclature, packaging, and technical details."
		price = 25 + r.Intn(90)
	}
	return name, tech, price
}
