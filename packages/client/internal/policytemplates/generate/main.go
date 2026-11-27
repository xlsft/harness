// Command generate writes the device management templates (package policytemplates) to a folder, deploy/mdm.
//
//	go run ./internal/policytemplates/generate <folder>
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/xlsft/harness/packages/client/internal/policytemplates"
	"github.com/xlsft/harness/packages/client/settings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: generate <folder>")
		os.Exit(2)
	}
	files, err := policytemplates.Generate(settings.Schema())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := filepath.Join(os.Args[1], filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(p, files[n], 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(p)
	}
}
