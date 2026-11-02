# Last product never shows up in the listing

**Reported by:** support@acme.example

Our catalog has 25 products. Listing them 10 per page:

```
$ go run ./cmd/catalog -n 25 -per-page 10 -page 3
```

shows only 4 products (P021 to P024). Product P025 never appears on any page.
I also tried a catalog with exactly 20 products and the second page shows 9
products instead of 10. The total page count looks right in both cases.
