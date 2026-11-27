package sources

import (
	"strings"

	"github.com/xlsft/harness/packages/client/importer"
)

// Cline, Roo Code and Kilo Code (the VS Code agents of one family). Folder level only so far.
//
// Cline: .clinerules/ or .cline/rules/ at the root, markdown rules with an optional `paths:` list (the same field
// harness rules use); a single .clinerules file is the older form (https://docs.cline.bot/features/cline-rules; the
// file is also listed by Zed, https://zed.dev/docs/ai/instructions).
// Roo Code: .roo/rules/ (all modes) and .roo/rules-{modeSlug}/ (one mode); legacy .roorules and .roorules-{modeSlug}
// files (https://roocodeinc.github.io/Roo-Code/features/custom-instructions).
// Kilo Code: .kilocode/rules/ (and .kilocode/rules-{mode}/), now also .kilo/rules/
// (https://kilo.ai/docs/customize/custom-rules).

func pathsRule(prefix string) func(importer.FolderFile) importer.Found {
	return func(f importer.FolderFile) importer.Found {
		md := string(f.Data)
		return rule(ruleName(f.Rel, prefix, ".md", ".txt"), md, globList(md, "paths"), "")
	}
}

// modeRule is a rule of one mode of Roo or Kilo: harness has no modes, so it always applies.
func modeRule(tool string) func(importer.FolderFile) importer.Found {
	return func(f importer.FolderFile) importer.Found {
		// ".roo/rules-code/x.md" → "code/x"
		rel := f.Rel
		if f.Dir != "" {
			rel = rel[len(f.Dir)+1:]
		}
		_, after, _ := strings.Cut(rel, "/rules-")
		r := rule(ruleName(after, "", ".md", ".txt"), string(f.Data), nil, "")
		r.Note = tool + " used it in one mode only; here it always applies"
		return r
	}
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "cline", Name: "Cline", Logo: "cline", Description: "A folder's Cline rules."},
		patterns: []importer.FolderPattern{
			{Glob: ".clinerules/*.md", Read: one(pathsRule(".clinerules/"))},
			{Glob: ".clinerules/*.txt", Read: one(pathsRule(".clinerules/"))},
			{Glob: ".cline/rules/*.md", Read: one(pathsRule(".cline/rules/"))},
			{Glob: ".clinerules", Read: instructions},
		},
	})
	register(&reader{
		info: importer.SourceInfo{ID: "roo", Name: "Roo Code", Logo: "roo", Description: "A folder's Roo Code rules."},
		patterns: []importer.FolderPattern{
			{Glob: ".roo/rules/*", Read: one(pathsRule(".roo/rules/"))},
			{Glob: ".roo/rules-*/*", Read: one(modeRule("Roo Code"))},
			{Glob: ".roorules", Read: instructions},
			{Glob: ".roorules-*", Read: func(f importer.FolderFile) []importer.Found {
				out := instructions(f)
				out[0].Note = "Roo Code used it in one mode only; here it always applies"
				return out
			}},
		},
	})
	register(&reader{
		info: importer.SourceInfo{ID: "kilo", Name: "Kilo Code", Logo: "kilo", Description: "A folder's Kilo Code rules."},
		patterns: []importer.FolderPattern{
			{Glob: ".kilocode/rules/*", Read: one(pathsRule(".kilocode/rules/"))},
			{Glob: ".kilocode/rules-*/*", Read: one(modeRule("Kilo Code"))},
			{Glob: ".kilo/rules/*", Read: one(pathsRule(".kilo/rules/"))},
		},
	})
}
