package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/documents"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/tools"
)

// Office files and PDFs are zip archives or binary streams, so a byte diff says nothing. Both versions are projected to
// addressed text and that is what gets diffed: the Changes pane shows what a reader would see change.

const (
	// maxDocumentDiffBytes bounds a document that gets compared by content, on either side. It is its own limit: documents
	// are compressed, and a few megabytes are ordinary.
	maxDocumentDiffBytes = 64 << 20
	// maxDocumentDiffLines bounds the projected lines of one side.
	maxDocumentDiffLines = 20000
	// documentHunkHeader marks the hunks as not being the file's bytes.
	documentHunkHeader = "compared by content"
)

// documentDiff compares two versions of a document by content. Without a version (nil) that side is empty. A version
// that cannot be read makes the result binary: nothing better can be said, and nothing false is.
func documentDiff(path string, old, cur []byte) *diffResult {
	project := func(data []byte) ([]string, bool) {
		if data == nil {
			return nil, true
		}
		lines, err := documents.ProjectBytes(path, data)
		if err != nil {
			return nil, false
		}
		if len(lines) > maxDocumentDiffLines {
			n := len(lines) - maxDocumentDiffLines
			lines = append(lines[:maxDocumentDiffLines:maxDocumentDiffLines], fmt.Sprintf("[%d more lines not compared]", n))
		}
		return lines, true
	}
	oldLines, ok := project(old)
	if !ok {
		return &diffResult{binary: true}
	}
	curLines, ok := project(cur)
	if !ok {
		return &diffResult{binary: true}
	}
	join := func(l []string) string {
		if len(l) == 0 {
			return ""
		}
		return strings.ToValidUTF8(strings.Join(l, "\n"), "�") + "\n"
	}
	hunks := tools.DiffLines(join(oldLines), join(curLines), diffContextLines)
	add, del := tools.DiffStats(hunks)
	r := &diffResult{additions: int32(add), deletions: int32(del)}
	for _, h := range hunks {
		r.hunks = append(r.hunks, &agentv1.FileDiffHunk{Header: documentHunkHeader, OldStart: int32(h.OldStart), OldLines: int32(h.OldLines), NewStart: int32(h.NewStart), NewLines: int32(h.NewLines), Lines: h.Lines})
	}
	return r
}

// diffFileLimit is how large a file may be and still get compared.
func diffFileLimit(path string) int64 {
	if documents.IsDocumentPath(path) {
		return maxDocumentDiffBytes
	}
	return maxDiffFileBytes
}

// repoDocument compares a tracked document's two sides, from git's objects and the disk. Git itself would call it binary.
func (b *diffBuilder) repoDocument(ctx context.Context, r *diffRepo, e rawDiffEntry, size int64) *agentv1.FileDiff {
	f := &agentv1.FileDiff{Path: r.abs(e.path), OldPath: r.absOld(e.oldPath), Status: e.status(), Source: agentv1.FileDiffSource_FILE_DIFF_SOURCE_GIT}
	k := diffCacheKey{path: filepath.Join(r.top, filepath.FromSlash(e.path)) + "\x00" + e.newOID, baseline: e.oldOID, size: size}
	full := filepath.Join(r.top, filepath.FromSlash(e.path))
	onDisk := r.kind != agentv1.DiffBaseKind_DIFF_BASE_KIND_COMMIT
	if onDisk {
		if st, err := os.Lstat(full); err == nil {
			k.mtime = st.ModTime()
		}
	}
	res := diffCacheGet(k)
	if res == nil {
		var oldData, curData []byte
		var err error
		if strings.Trim(e.oldOID, "0") != "" {
			if oldData, err = diffGit(ctx, r.top, "", "cat-file", "blob", e.oldOID); err != nil {
				f.Binary = true
				return f
			}
		}
		switch {
		case onDisk:
			if data, err := os.ReadFile(full); err == nil {
				curData = data
			}
		case strings.Trim(e.newOID, "0") != "":
			if curData, err = diffGit(ctx, r.top, "", "cat-file", "blob", e.newOID); err != nil {
				f.Binary = true
				return f
			}
		}
		res = documentDiff(e.path, oldData, curData)
		diffCachePut(k, res)
	}
	f.Binary, f.Hunks, f.Additions, f.Deletions = res.binary, res.hunks, res.additions, res.deletions
	return f
}
