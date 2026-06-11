package extraction

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPartsBaseExtractorFetchSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("expected bearer auth header, got %q", got)
		}
		if got := r.URL.Query().Get("partNumber"); got == "" {
			t.Fatalf("expected partNumber query param to be set")
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ValuesPerCode": []map[string]any{
				{
					"ConditionCode": "NE",
					"MinUnitPrice":  12.5,
					"MaxUnitPrice":  18.0,
					"LastUpdated":   "2026-06-10",
				},
				{
					"ConditionCode": "SV",
					"MinUnitPrice":  8.0,
					"MaxUnitPrice":  11.0,
				},
			},
			"results": []map[string]any{
				{
					"SupplierName": "Vendor A",
					"Manufacturer": "Acme Aero",
					"PartNumber":   "PN-123",
					"ConditionCode": "NE",
					"UnitPrice":    15.25,
					"UPC":          "123456789012",
				},
				{
					"SupplierName": "Vendor B",
					"Manufacturer": "Acme Aero",
					"PartNumber":   "PN-456",
					"ConditionCode": "SV",
					"UnitPrice":    "9.75",
				},
			},
		})
	}))
	defer server.Close()

	extractor := NewPartsBaseExtractor(PartsBaseConfig{
		Enabled:           true,
		APIKey:            "test-key",
		BaseURL:           server.URL,
		MarketPricingPath: "/api-market-pricing",
		TimeoutSeconds:    5,
	})

	snaps, err := extractor.Fetch(context.Background(), "8415016107327", nil)
	if err != nil {
		t.Fatalf("fetch returned error: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}

	raw := snaps[0].RawResponse
	if got := raw["data_source"]; got != "live_partsbase_market_pricing" {
		t.Fatalf("expected live partsbase data source, got %#v", got)
	}
	if got := intFromAny(raw["result_count"]); got <= 0 {
		t.Fatalf("expected positive result_count, got %d", got)
	}
	if got := intFromAny(raw["supplier_count"]); got != 2 {
		t.Fatalf("expected supplier_count=2, got %d", got)
	}
	if refs := mapSliceFromAny(raw["commercial_references"]); len(refs) == 0 {
		t.Fatalf("expected commercial_references to be populated")
	}
}

func TestPartsBaseExtractorFetchUnavailable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()

	extractor := NewPartsBaseExtractor(PartsBaseConfig{
		Enabled:           true,
		APIKey:            "test-key",
		BaseURL:           server.URL,
		MarketPricingPath: "/api-market-pricing",
		TimeoutSeconds:    5,
	})

	snaps, err := extractor.Fetch(context.Background(), "5120008785932", nil)
	if err != nil {
		t.Fatalf("expected graceful fallback without error, got: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 fallback snapshot, got %d", len(snaps))
	}
	if got := snaps[0].RawResponse["data_source"]; got != "partsbase_unavailable" {
		t.Fatalf("expected partsbase_unavailable data source, got %#v", got)
	}
	if snaps[0].QualityScore >= 0.5 {
		t.Fatalf("expected degraded quality score on fallback, got %f", snaps[0].QualityScore)
	}
}

func TestPartsBaseExtractorWithoutAPIKeyReturnsNoSnapshots(t *testing.T) {
	t.Parallel()

	extractor := NewPartsBaseExtractor(PartsBaseConfig{
		Enabled: true,
		APIKey:  "",
	})

	snaps, err := extractor.Fetch(context.Background(), "8540013800690", nil)
	if err != nil {
		t.Fatalf("fetch returned error: %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("expected no snapshots without API key, got %d", len(snaps))
	}
}
