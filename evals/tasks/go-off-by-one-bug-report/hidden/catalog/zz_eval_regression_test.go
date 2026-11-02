package catalog

import "testing"

func TestEvalLastPage(t *testing.T) {
	cases := []struct {
		n, size, page, want int
		last                string
	}{
		{25, 10, 3, 5, "P025"},
		{20, 10, 2, 10, "P020"},
		{1, 10, 1, 1, "P001"},
		{7, 3, 3, 1, "P007"},
		{9, 3, 2, 3, "P006"},
	}
	for _, c := range cases {
		got, _ := Page(Generate(c.n), c.page, c.size)
		if len(got) != c.want || got[len(got)-1].SKU != c.last {
			t.Errorf("n=%d size=%d page=%d: got %d items %v, want %d ending with %s", c.n, c.size, c.page, len(got), got, c.want, c.last)
		}
	}
	if got, pages := Page(Generate(0), 1, 10); got != nil || pages != 0 {
		t.Errorf("empty catalog: got %v, %d pages", got, pages)
	}
}
