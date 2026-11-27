package sources

import (
	"github.com/xlsft/harness/packages/client/importer"
)

// Tools whose project files are a single instructions file or a rules folder. Folder level only so far; each moves to
// a file of its own when it gets a user-level reader.
//
// Kiro: .kiro/steering/*.md, frontmatter inclusion always (default) / fileMatch with fileMatchPattern / manual / auto
// (https://kiro.dev/docs/steering/).
// Continue: .continue/rules/*.md, frontmatter name, globs, alwaysApply, description
// (https://docs.continue.dev/customize/deep-dives/rules).
// Junie: .junie/guidelines.md (the IDE plugin's default) and .junie/AGENTS.md
// (https://junie.jetbrains.com/docs/guidelines-and-memory.html).
// Zed: .rules at the root, first of Zed's list (https://zed.dev/docs/ai/instructions); the other names on that list
// belong to the tools above.
// Aider: CONVENTIONS.md, loaded with --read or `read:` in .aider.conf.yml (https://aider.chat/docs/usage/conventions.html).

func kiroSteering(f importer.FolderFile) importer.Found {
	md := string(f.Data)
	name := ruleName(f.Rel, ".kiro/steering/", ".md")
	switch fm(md, "inclusion") {
	case "", "always":
		return rule(name, md, nil, "")
	case "fileMatch":
		return rule(name, md, globList(md, "fileMatchPattern"), "")
	case "manual":
		return rule(name, md, nil, "Kiro used it only when referenced with #")
	}
	return rule(name, md, nil, "Kiro used it when the request matched its description")
}

func continueRule(f importer.FolderFile) importer.Found {
	md := string(f.Data)
	name := ruleName(f.Rel, ".continue/rules/", ".md")
	if fm(md, "alwaysApply") == "true" {
		return rule(name, md, nil, "")
	}
	onDemand := ""
	if fm(md, "alwaysApply") == "false" {
		onDemand = "Continue used it when the agent judged it relevant"
	}
	return rule(name, md, globList(md, "globs"), onDemand)
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "kiro", Name: "Kiro", Logo: "kiro", Description: "A folder's Kiro steering files."},
		patterns: []importer.FolderPattern{
			{Glob: ".kiro/steering/*.md", Read: one(kiroSteering)},
		},
	})
	register(&reader{
		info: importer.SourceInfo{ID: "continue", Name: "Continue", Logo: "continue", Description: "A folder's Continue rules."},
		patterns: []importer.FolderPattern{
			{Glob: ".continue/rules/*.md", Read: one(continueRule)},
		},
	})
	register(&reader{
		info: importer.SourceInfo{ID: "junie", Name: "Junie", Logo: "junie", Description: "A folder's Junie guidelines."},
		patterns: []importer.FolderPattern{
			{Glob: ".junie/guidelines.md", Read: instructions},
			{Glob: ".junie/AGENTS.md", Read: instructions},
		},
	})
	register(&reader{
		info:     importer.SourceInfo{ID: "zed", Name: "Zed", Logo: "zed", Description: "A folder's .rules file."},
		patterns: []importer.FolderPattern{{Glob: ".rules", Read: instructions}},
	})
	register(&reader{
		info:     importer.SourceInfo{ID: "aider", Name: "Aider", Logo: "aider", Description: "A folder's CONVENTIONS.md."},
		patterns: []importer.FolderPattern{{Glob: "CONVENTIONS.md", Read: instructions}},
	})
}
