package documents

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Verification compares a new version with the original. Three states matter: what the change broke, what was already
// broken, and what could not be checked at all. "Could not verify" is never reported as a pass.

// NotOfficeProof says, where a pass could be misread, how far a pass goes.
const NotOfficeProof = "These checks read the file against the format's rules; they do not prove that Word, Excel or PowerPoint opens it without a repair prompt."

// Verification is the result of comparing a new version with the original.
type Verification struct {
	New, Inherited, Unverified []Finding
}

// HasNewErrors reports whether the change introduced a defect in the file.
func (v *Verification) HasNewErrors() bool {
	for _, f := range v.New {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Verify checks a new version on a temporary copy, against the original's findings. orig is nil for a new file. dir is
// where the copy goes, so verification never touches the user's file.
func Verify(name string, orig, updated []byte, dir string) *Verification {
	v := &Verification{}
	tmp, err := os.CreateTemp(dir, "harness-verify-*"+filepath.Ext(name))
	if err != nil {
		v.Unverified = append(v.Unverified, Finding{SeverityUnverified, "verify:temp", "the temporary copy could not be written: " + err.Error()})
		return v
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.Write(updated)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		v.Unverified = append(v.Unverified, Finding{SeverityUnverified, "verify:temp", "the temporary copy could not be written"})
		return v
	}
	copyBytes, err := os.ReadFile(tmp.Name())
	if err != nil {
		v.Unverified = append(v.Unverified, Finding{SeverityUnverified, "verify:temp", "the temporary copy could not be read"})
		return v
	}
	doc, err := Open(name, copyBytes)
	if err != nil {
		v.New = append(v.New, Finding{SeverityError, "verify:open", "the new version does not open: " + err.Error()})
		return v
	}
	after := doc.Check()
	before := map[string]bool{}
	if orig != nil {
		if od, err := Open(name, orig); err == nil {
			for _, f := range od.Check() {
				before[f.Key] = true
			}
		}
	}
	for _, f := range after {
		switch {
		case f.Severity == SeverityUnverified:
			v.Unverified = append(v.Unverified, f)
		case before[f.Key]:
			v.Inherited = append(v.Inherited, f)
		default:
			v.New = append(v.New, f)
		}
	}
	return v
}

// CheckOne runs the checks on a file by itself, with nothing to compare with.
func CheckOne(doc Document) *Verification {
	v := &Verification{}
	for _, f := range doc.Check() {
		if f.Severity == SeverityUnverified {
			v.Unverified = append(v.Unverified, f)
		} else {
			v.Inherited = append(v.Inherited, f)
		}
	}
	return v
}

func writeFindings(b *strings.Builder, title string, fs []Finding, max int) {
	if len(fs) == 0 {
		return
	}
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Severity < fs[j].Severity })
	fmt.Fprintf(b, "%s (%d):\n", title, len(fs))
	for i, f := range fs {
		if i == max {
			fmt.Fprintf(b, "  … %d more\n", len(fs)-max)
			break
		}
		fmt.Fprintf(b, "  [%s] %s\n", f.Severity, f.Message)
	}
}

// Report renders a verification. standalone is for a check of a file by itself, where nothing can be "new".
func (v *Verification) Report(standalone bool) string {
	var b strings.Builder
	if standalone {
		writeFindings(&b, "Findings", v.Inherited, 40)
	} else {
		writeFindings(&b, "New problems from this change", v.New, 40)
		writeFindings(&b, "Already in the original", v.Inherited, 10)
	}
	writeFindings(&b, "Could not verify", v.Unverified, 20)
	if b.Len() == 0 {
		b.WriteString("Checks found nothing.\n")
	}
	b.WriteString(NotOfficeProof)
	return b.String()
}

// ProjectBytes projects a file for the Changes pane; nil and an error when it cannot be opened.
func ProjectBytes(name string, data []byte) (lines []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			lines, err = nil, fmt.Errorf("cannot read it: %v", r)
		}
	}()
	doc, err := Open(name, data)
	if err != nil {
		return nil, err
	}
	return doc.Project(), nil
}
