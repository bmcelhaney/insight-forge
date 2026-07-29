package extraction

import (
	"encoding/json"
	"testing"
)

func TestParseAbilityOneCommercePayload(t *testing.T) {
	raw := `{
	  "resultsList": {
	    "records": [{
	      "records": [{
	        "attributes": {
	          "sku.activePrice": ["13.010000"],
	          "sku.listPrice": ["13.010000"],
	          "sku.displayName": ["U.S. Government Pen - Medium Point - Black Ink"],
	          "sku.repositoryId": ["7520-00-935-7136"],
	          "product.brand": ["SKILCRAFT&reg;"]
	        }
	      }]
	    }]
	  }
	}`
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	hits := parseAbilityOneCommercePayload(payload)
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].SKU != "7520-00-935-7136" {
		t.Fatalf("sku %q", hits[0].SKU)
	}
	if hits[0].Price < 13.0 || hits[0].Price > 13.02 {
		t.Fatalf("price %v", hits[0].Price)
	}
	if hits[0].Brand != "SKILCRAFT" {
		t.Fatalf("brand %q", hits[0].Brand)
	}
}

func TestFormatDashedNSN(t *testing.T) {
	if got := formatDashedNSN("7520009357136"); got != "7520-00-935-7136" {
		t.Fatalf("got %q", got)
	}
	if got := abilityOneSearchTerm("7520009357136"); got != "7520-00-935-7136" {
		t.Fatalf("term %q", got)
	}
}

func TestAbilityOneIDsMatch(t *testing.T) {
	if !abilityOneIDsMatch("7520-00-935-7136", "7520009357136") {
		t.Fatal("expected match")
	}
	if abilityOneIDsMatch("8105-01-517-1352", "7520009357136") {
		t.Fatal("expected no match")
	}
}
