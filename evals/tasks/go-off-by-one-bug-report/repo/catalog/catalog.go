// Package catalog holds the product listing logic.
package catalog

import "fmt"

// Product is a catalog entry.
type Product struct {
	SKU   string
	Price int // cents
}

// Generate returns n demo products P001..Pnnn.
func Generate(n int) []Product {
	out := make([]Product, n)
	for i := range out {
		out[i] = Product{SKU: fmt.Sprintf("P%03d", i+1), Price: 100 * (i + 1)}
	}
	return out
}
