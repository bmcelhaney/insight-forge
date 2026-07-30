package processing

import (
	"strings"
	"testing"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

func TestResolveItemIdentityPrefersETSOverWebFLISPrototype(t *testing.T) {
	snaps := []models.DataSnapshot{
		{
			SourceCode: "WEBFLIS",
			RawResponse: map[string]any{
				"item_name":                 "CLEANING, PACKAGING OR PERSONAL CARE ITEM",
				"technical_characteristics": "Janitorial, packaging or personal care consumable/supply.",
			},
		},
		{
			SourceCode: "ABILITYONE_ETS",
			RawResponse: map[string]any{
				"abilityone_descriptions": []string{
					"Cover, Paint Roller, 9 inches, Woven fabric, 1/2 inches Nap, EA",
				},
				"commercial_descriptions": []string{
					"WOOSTER Paint Roller Cover 9in",
				},
			},
		},
	}
	id := resolveItemIdentity(snaps)
	if !strings.Contains(strings.ToLower(id.Name), "paint roller") {
		t.Fatalf("expected paint roller identity, got %q (source=%s)", id.Name, id.Source)
	}
	if id.Source != "ABILITYONE_ETS" {
		t.Fatalf("expected ABILITYONE_ETS source, got %s", id.Source)
	}
	if isPrototypeGenericItemName(id.Name) {
		t.Fatalf("real product name should not be treated as prototype generic: %q", id.Name)
	}
}

func TestIsPrototypeGenericItemName(t *testing.T) {
	if !isPrototypeGenericItemName("CLEANING, PACKAGING OR PERSONAL CARE ITEM") {
		t.Fatal("expected generic category to be detected")
	}
	if isPrototypeGenericItemName("Cover, Paint Roller, 9 inches, Woven fabric") {
		t.Fatal("real nomenclature should not be generic")
	}
	if isPrototypeGenericItemName("PEN, BALL-POINT, BLACK, MEDIUM POINT") {
		t.Fatal("federal-style comma nomenclature should not be generic")
	}
}

func TestAlignRichAnalysisToIdentityRewritesSummary(t *testing.T) {
	rich := RichAnalysis{
		Summary: "CLEANING, PACKAGING OR PERSONAL CARE ITEM (NSN 8020015964250) shows sourcing attractiveness of 88 with supply risk at 19. Extra narrative stays.",
		FullReport: `DYNAMIC SYNTHESIS — NSN 8020015964250
CLEANING, PACKAGING OR PERSONAL CARE ITEM (FSC 8020)

QUANTITATIVE HIGHLIGHTS
- Sourcing Attractiveness: 88`,
	}
	id := itemIdentity{Name: "Cover, Paint Roller, 9 inches", Source: "ABILITYONE_ETS"}
	out := alignRichAnalysisToIdentity(rich, id, "8020015964250", 88, 19)
	if strings.Contains(out.Summary, "CLEANING, PACKAGING") {
		t.Fatalf("summary still has wrong category: %q", out.Summary)
	}
	if !strings.Contains(out.Summary, "Cover, Paint Roller") {
		t.Fatalf("summary missing real name: %q", out.Summary)
	}
	if strings.Contains(out.FullReport, "CLEANING, PACKAGING OR PERSONAL CARE ITEM (FSC 8020)") {
		t.Fatalf("report still has wrong title: %q", out.FullReport)
	}
	if !strings.Contains(out.FullReport, "Cover, Paint Roller") {
		t.Fatalf("report missing real name: %q", out.FullReport)
	}
}
