package sources

import (
	"github.com/xlsft/harness/packages/client/importer"
)

// Windsurf (Cascade, now Devin Desktop). Folder level only so far.
//
// Workspace rules: .devin/rules/*.md (native) and .windsurf/rules/*.md, one rule per file, frontmatter trigger
// (always_on, glob, model_decision, manual) and globs; legacy .windsurfrules at the root
// (https://docs.devin.ai/desktop/cascade/memories, formerly https://docs.windsurf.com/windsurf/cascade/memories).

func windsurfRule(prefix string) func(importer.FolderFile) importer.Found {
	return func(f importer.FolderFile) importer.Found {
		md := string(f.Data)
		name := ruleName(f.Rel, prefix, ".md")
		switch fm(md, "trigger") {
		case "", "always_on":
			return rule(name, md, nil, "")
		case "glob":
			return rule(name, md, globList(md, "globs"), "")
		case "manual":
			return rule(name, md, nil, "Windsurf used it only when mentioned")
		}
		return rule(name, md, nil, "Windsurf used it when it judged it relevant")
	}
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "windsurf", Name: "Windsurf", Logo: "windsurf",
			Description: "A folder's Windsurf rules and .windsurfrules."},
		patterns: []importer.FolderPattern{
			{Glob: ".windsurf/rules/*.md", Read: one(windsurfRule(".windsurf/rules/"))},
			{Glob: ".devin/rules/*.md", Read: one(windsurfRule(".devin/rules/"))},
			{Glob: ".windsurfrules", Read: instructions},
		},
	})
}
