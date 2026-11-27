package sources

import (
	"context"

	"github.com/xlsft/harness/packages/client/importer"
)

// FolderID is the Any folder source: one walk of a chosen folder that finds the project files of every registered
// source, by their own FolderPatterns. A reader that adds a pattern adds it here too; nothing is listed twice. AGENTS.md
// is harness's own and is never imported; Claude Code's folder files have their own import (importer.RunProject).
const FolderID = "folder"

type anyFolder struct{}

func (anyFolder) Info() importer.SourceInfo {
	return importer.SourceInfo{ID: FolderID, Name: "Any folder", Folder: true,
		Description: "Every other tool's instructions, rules and MCP configs in a folder, in one pass."}
}

func (anyFolder) Detect(importer.Env) bool { return false }

func (anyFolder) ScanUser(_ context.Context, _ importer.Env) ([]importer.Found, error) {
	return nil, nil
}

// FolderPatterns are the patterns of all other sources, in their order. The Any folder source itself is not among
// them, so the list is built when asked.
func (anyFolder) FolderPatterns() []importer.FolderPattern {
	var out []importer.FolderPattern
	for _, s := range All() {
		if s.Info().ID == FolderID {
			continue
		}
		out = append(out, s.FolderPatterns()...)
	}
	return out
}

func init() { register(anyFolder{}) }
