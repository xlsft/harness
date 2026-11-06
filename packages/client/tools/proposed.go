package tools

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// maxProposedBytes is the largest side of a proposed edit shown as a diff.
const maxProposedBytes = 4 << 20

// ProposedEdit is what a call of a file tool would do to a file, worked out without doing it.
type ProposedEdit struct {
	// Path is absolute. Exists is false when the call creates the file.
	Path          string
	Exists        bool
	Before, After string
}

// ProposeEdit works out the change an Edit or Write call would make, for showing. An error means there is nothing to
// show (the call would fail, or the file is not text); the tool's own run stays the judge of whether a call is valid.
func ProposeEdit(toolName string, input json.RawMessage) (ProposedEdit, error) {
	var in struct {
		FilePath   string  `json:"file_path"`
		Content    *string `json:"content"`
		OldString  *string `json:"old_string"`
		NewString  *string `json:"new_string"`
		ReplaceAll bool    `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return ProposedEdit{}, err
	}
	if !filepath.IsAbs(in.FilePath) {
		return ProposedEdit{}, errors.New("file_path is not absolute")
	}
	p := ProposedEdit{Path: filepath.Clean(in.FilePath)}
	st, err := os.Stat(p.Path)
	switch {
	case err == nil && st.IsDir():
		return ProposedEdit{}, errors.New("a directory")
	case err == nil && st.Size() > maxProposedBytes:
		return ProposedEdit{}, errors.New("too large")
	case err == nil:
		data, err := os.ReadFile(p.Path)
		if err != nil {
			return ProposedEdit{}, err
		}
		if !utf8.Valid(data) {
			return ProposedEdit{}, errors.New("not text")
		}
		p.Exists, p.Before = true, string(data)
	case !os.IsNotExist(err):
		return ProposedEdit{}, err
	}
	switch toolName {
	case "Write":
		if in.Content == nil {
			return ProposedEdit{}, errors.New("no content")
		}
		p.After = *in.Content
	case "Edit":
		if in.OldString == nil || in.NewString == nil {
			return ProposedEdit{}, errors.New("no strings")
		}
		switch oldS := *in.OldString; {
		case oldS == "" && strings.TrimSpace(p.Before) != "":
			return ProposedEdit{}, errors.New("the file is not empty")
		case oldS == "":
			p.After = *in.NewString
		case !p.Exists:
			return ProposedEdit{}, errors.New("no such file")
		default:
			n := strings.Count(p.Before, oldS)
			if n == 0 || (n > 1 && !in.ReplaceAll) {
				return ProposedEdit{}, errors.New("the string does not match once")
			}
			if in.ReplaceAll {
				p.After = strings.ReplaceAll(p.Before, oldS, *in.NewString)
			} else {
				p.After = strings.Replace(p.Before, oldS, *in.NewString, 1)
			}
		}
	default:
		return ProposedEdit{}, errors.New("not a tool with a diff")
	}
	if len(p.After) > maxProposedBytes || !utf8.ValidString(p.After) {
		return ProposedEdit{}, errors.New("too large or not text")
	}
	return p, nil
}
