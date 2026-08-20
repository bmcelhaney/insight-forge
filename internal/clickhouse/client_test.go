package clickhouse

import "testing"

func TestJoinHostPort(t *testing.T) {
	got, err := joinHostPort("https://igijlwqd6s.eastus2.azure.clickhouse.cloud", "8443")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://igijlwqd6s.eastus2.azure.clickhouse.cloud:8443"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	got, err = joinHostPort("igijlwqd6s.eastus2.azure.clickhouse.cloud", "8443")
	if err != nil || got != want {
		t.Fatalf("bare host: %q %v", got, err)
	}
}
