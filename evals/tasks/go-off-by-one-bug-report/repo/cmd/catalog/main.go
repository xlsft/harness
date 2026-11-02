// Command catalog prints one page of the demo product catalog.
package main

import (
	"flag"
	"fmt"

	"example.com/catalog/catalog"
)

func main() {
	n := flag.Int("n", 25, "number of products")
	per := flag.Int("per-page", 10, "products per page")
	page := flag.Int("page", 1, "page number (1-based)")
	flag.Parse()

	items, pages := catalog.Page(catalog.Generate(*n), *page, *per)
	fmt.Printf("page %d of %d\n", *page, pages)
	for _, p := range items {
		fmt.Printf("%s\t%d.%02d\n", p.SKU, p.Price/100, p.Price%100)
	}
}
