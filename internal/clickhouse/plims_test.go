package clickhouse

import "testing"

func TestDigitsOnlyAndValidNSN(t *testing.T) {
	if got := digitsOnly("8410-01-449-5286"); got != "8410014495286" {
		t.Fatalf("got %q", got)
	}
	if validAnalyzeNSN(digitsOnly("8950-01-E62-2667")) {
		t.Fatal("alpha NIIN fragment should be rejected")
	}
	if !validAnalyzeNSN("8410014495286") || !validAnalyzeNSN("014495286") {
		t.Fatal("expected 13- and 9-digit NSNs to be valid")
	}
}
