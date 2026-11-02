package catalog

import "testing"

func TestPageFirst(t *testing.T) {
	items := Generate(25)
	got, pages := Page(items, 1, 10)
	if pages != 3 {
		t.Fatalf("pages = %d, want 3", pages)
	}
	if len(got) != 10 || got[0].SKU != "P001" || got[9].SKU != "P010" {
		t.Fatalf("unexpected first page: %v", got)
	}
}

func TestPageOutOfRange(t *testing.T) {
	if got, _ := Page(Generate(5), 2, 10); got != nil {
		t.Fatalf("expected no items, got %v", got)
	}
}
