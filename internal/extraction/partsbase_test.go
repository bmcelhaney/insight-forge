package extraction

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPartsBaseExtractorFetchSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/connect/token":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST for token endpoint, got %s", r.Method)
			}
			if got := r.Header.Get("Content-Type"); !strings.Contains(got, "application/x-www-form-urlencoded") {
				t.Fatalf("expected form encoded token request, got %q", got)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("failed to parse form body: %v", err)
			}
			if got := r.Form.Get("grant_type"); got != "password" {
				t.Fatalf("expected grant_type=password, got %q", got)
			}
			if got := r.Form.Get("client_id"); got != "client-id" {
				t.Fatalf("expected client_id=client-id, got %q", got)
			}
			if got := r.Form.Get("client_secret"); got != "client-secret" {
				t.Fatalf("expected client_secret=client-secret, got %q", got)
			}
			if got := r.Form.Get("username"); got != "user" {
				t.Fatalf("expected username=user, got %q", got)
			}
			if got := r.Form.Get("password"); got != "pass" {
				t.Fatalf("expected password=pass, got %q", got)
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "test-token",
				"expires_in":   3600,
				"token_type":   "Bearer",
			})
		case "/api/data/GovData":
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("expected bearer token auth header, got %q", got)
			}
			if got := r.URL.Query().Get("Filter"); got != "8415016107327" {
				t.Fatalf("expected Filter query value, got %q", got)
			}
			if got := r.URL.Query().Get("Type"); got != "Nsn" {
				t.Fatalf("expected Type=Nsn, got %q", got)
			}
			if got := r.URL.Query().Get("startDate"); got != "2000-01-01" {
				t.Fatalf("expected startDate=2000-01-01, got %q", got)
			}
			if got := r.URL.Query()["Section"]; len(got) != 2 || !containsAll(got, []string{"Procurement", "NsnId"}) {
				t.Fatalf("expected Section values [Procurement NsnId], got %v", got)
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"procurement": []map[string]any{
					{
						"unitPrice":  "15.25",
						"vendor":     "Vendor A",
						"contractNo": "SPE1M1-26-P-001",
						"awardDate":  "2026/05",
						"quantity":   "20",
						"cage":       "1A2B3",
					},
					{
						"unitPrice":  10.0,
						"vendor":     "Vendor B",
						"contractNo": "SPE1M1-26-P-002",
						"awardDate":  "2026/04",
						"quantity":   12,
						"cage":       "4D5E6",
					},
				},
				"nsnId": []map[string]any{
					{"description": "TEST ITEM DESCRIPTION"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	extractor := NewPartsBaseExtractor(PartsBaseConfig{
		Enabled:          true,
		ClientID:         "client-id",
		ClientSecret:     "client-secret",
		Username:         "user",
		Password:         "pass",
		AuthURL:          server.URL + "/connect/token",
		BaseURL:          server.URL,
		GovDataPath:      "/api/data/GovData",
		GovDataType:      "Nsn",
		GovDataStartDate: "2000-01-01",
		GovDataSections:  []string{"Procurement", "NsnId"},
		OAuthGrantType:   "password",
		OAuthScope:       "api",
		TimeoutSeconds:   5,
	})

	snaps, err := extractor.Fetch(context.Background(), "8415016107327", nil)
	if err != nil {
		t.Fatalf("fetch returned error: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}

	raw := snaps[0].RawResponse
	if got := raw["data_source"]; got != "live_partsbase_govdata" {
		t.Fatalf("expected live govdata source, got %#v", got)
	}
	if got := intFromAny(raw["result_count"]); got != 2 {
		t.Fatalf("expected result_count=2, got %d", got)
	}
	if got := intFromAny(raw["supplier_count"]); got != 2 {
		t.Fatalf("expected supplier_count=2, got %d", got)
	}
	if got := strings.TrimSpace(firstNonEmptyString(raw["nsn_description"])); got != "TEST ITEM DESCRIPTION" {
		t.Fatalf("expected nsn_description to be present, got %q", got)
	}
	if signals := mapSliceFromAny(raw["price_signals"]); len(signals) == 0 {
		t.Fatalf("expected price_signals to be populated")
	}
	if refs := mapSliceFromAny(raw["commercial_references"]); len(refs) == 0 {
		t.Fatalf("expected commercial_references to be populated")
	}
	if reqURL := strings.TrimSpace(firstNonEmptyString(raw["request_url"])); reqURL == "" {
		t.Fatalf("expected request_url to be populated")
	}
}

func TestPartsBaseExtractorCachesOAuthToken(t *testing.T) {
	t.Parallel()

	var tokenCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/connect/token":
			atomic.AddInt32(&tokenCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "cached-token",
				"expires_in":   3600,
				"token_type":   "Bearer",
			})
		case "/api/data/GovData":
			if got := r.Header.Get("Authorization"); got != "Bearer cached-token" {
				t.Fatalf("expected bearer token auth header, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"procurement": []map[string]any{
					{"unitPrice": 5.5, "vendor": "Vendor A"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	extractor := NewPartsBaseExtractor(PartsBaseConfig{
		Enabled:          true,
		ClientID:         "client-id",
		ClientSecret:     "client-secret",
		Username:         "user",
		Password:         "pass",
		AuthURL:          server.URL + "/connect/token",
		BaseURL:          server.URL,
		GovDataPath:      "/api/data/GovData",
		GovDataType:      "Nsn",
		GovDataStartDate: "2000-01-01",
		GovDataSections:  []string{"Procurement", "NsnId"},
		OAuthGrantType:   "password",
		OAuthScope:       "api",
		TimeoutSeconds:   5,
	})

	if _, err := extractor.Fetch(context.Background(), "8415016107327", nil); err != nil {
		t.Fatalf("first fetch returned error: %v", err)
	}
	if _, err := extractor.Fetch(context.Background(), "8415016107327", nil); err != nil {
		t.Fatalf("second fetch returned error: %v", err)
	}

	if calls := atomic.LoadInt32(&tokenCalls); calls != 1 {
		t.Fatalf("expected token endpoint to be called once, got %d", calls)
	}
}

func TestPartsBaseExtractorFetchUnavailable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/connect/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "test-token",
				"expires_in":   3600,
				"token_type":   "Bearer",
			})
		case "/api/data/GovData":
			http.Error(w, "upstream unavailable", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	extractor := NewPartsBaseExtractor(PartsBaseConfig{
		Enabled:          true,
		ClientID:         "client-id",
		ClientSecret:     "client-secret",
		Username:         "user",
		Password:         "pass",
		AuthURL:          server.URL + "/connect/token",
		BaseURL:          server.URL,
		GovDataPath:      "/api/data/GovData",
		GovDataType:      "Nsn",
		GovDataStartDate: "2000-01-01",
		GovDataSections:  []string{"Procurement", "NsnId"},
		OAuthGrantType:   "password",
		OAuthScope:       "api",
		TimeoutSeconds:   5,
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

func TestPartsBaseExtractorWithoutCredentialsReturnsNoSnapshots(t *testing.T) {
	t.Parallel()

	extractor := NewPartsBaseExtractor(PartsBaseConfig{
		Enabled: true,
	})

	snaps, err := extractor.Fetch(context.Background(), "8540013800690", nil)
	if err != nil {
		t.Fatalf("fetch returned error: %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("expected no snapshots without credentials, got %d", len(snaps))
	}
}

func containsAll(values []string, required []string) bool {
	seen := make(map[string]bool)
	for _, value := range values {
		seen[value] = true
	}
	for _, item := range required {
		if !seen[item] {
			return false
		}
	}
	return true
}