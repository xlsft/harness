package sources

import (
	"github.com/xlsft/harness/packages/client/importer"
)

// Aider: CONVENTIONS.md, loaded with --read or `read:` in .aider.conf.yml (https://aider.chat/docs/usage/conventions.html).
// Folder level only.

func init() {
	register(&reader{
		info:     importer.SourceInfo{ID: "aider", Name: "Aider", Logo: "aider", Description: "A folder's CONVENTIONS.md."},
		patterns: []importer.FolderPattern{{Glob: "CONVENTIONS.md", Read: instructions}},
	})
}
