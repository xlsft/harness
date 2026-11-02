package catalog

import "testing"

func TestPageLast(t *testing.T) {
	got, _ := Page(Generate(25), 3, 10)
	if len(got) != 5 || got[4].SKU != "P025" {
		t.Fatalf("unexpected last page: %v", got)
	}
}
