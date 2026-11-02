package catalog

// Page returns the items on the 1-based page `page` when showing `size`
// items per page, together with the total number of pages.
// Out-of-range pages return no items.
func Page[T any](items []T, page, size int) ([]T, int) {
	if size <= 0 {
		return nil, 0
	}
	pages := (len(items) + size - 1) / size
	if page < 1 || page > pages {
		return nil, pages
	}
	start := (page - 1) * size
	end := start + size
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], pages
}
