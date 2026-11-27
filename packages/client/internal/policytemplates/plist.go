package policytemplates

import (
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
)

// plistValue writes v, decoded from JSON, as XML property list elements at the given indent (tabs).
func plistValue(b *strings.Builder, v any, indent int) {
	pad := strings.Repeat("\t", indent)
	switch x := v.(type) {
	case nil:
		b.WriteString(pad + "<string></string>\n")
	case bool:
		if x {
			b.WriteString(pad + "<true/>\n")
		} else {
			b.WriteString(pad + "<false/>\n")
		}
	case json.Number:
		if n, err := x.Int64(); err == nil {
			fmt.Fprintf(b, "%s<integer>%d</integer>\n", pad, n)
		} else {
			fmt.Fprintf(b, "%s<real>%s</real>\n", pad, x)
		}
	case string:
		fmt.Fprintf(b, "%s<string>%s</string>\n", pad, html.EscapeString(x))
	case []any:
		if len(x) == 0 {
			b.WriteString(pad + "<array/>\n")
			return
		}
		b.WriteString(pad + "<array>\n")
		for _, e := range x {
			plistValue(b, e, indent+1)
		}
		b.WriteString(pad + "</array>\n")
	case map[string]any:
		plistDict(b, x, indent)
	default:
		panic(fmt.Sprintf("policytemplates: no plist form for %T", v))
	}
}

// plistDict writes a dictionary with its keys in order.
func plistDict(b *strings.Builder, m map[string]any, indent int) {
	pad := strings.Repeat("\t", indent)
	if len(m) == 0 {
		b.WriteString(pad + "<dict/>\n")
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString(pad + "<dict>\n")
	for _, k := range keys {
		fmt.Fprintf(b, "%s\t<key>%s</key>\n", pad, html.EscapeString(k))
		plistValue(b, m[k], indent+1)
	}
	b.WriteString(pad + "</dict>\n")
}

const plistHeader = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
`

// plistDocument is a whole property list of one dictionary.
func plistDocument(m map[string]any) string {
	var b strings.Builder
	b.WriteString(plistHeader)
	plistDict(&b, m, 0)
	b.WriteString("</plist>\n")
	return b.String()
}
