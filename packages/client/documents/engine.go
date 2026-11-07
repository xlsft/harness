package documents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The optional engine is LibreOffice found on the machine: a separate process, never linked. It works on a temporary
// copy with a private profile that has macros off and link updates off, under the session's sandbox when one is active.

// EngineTimeout bounds one conversion; a first start with a new profile takes the longest.
const EngineTimeout = 150 * time.Second

// ErrNoEngine says rendering or recalculation has nothing to run on. It is never reported as a pass.
var ErrNoEngine = errors.New("not available: LibreOffice was not found on this machine (looked for soffice on PATH and in the usual install places). Reads, edits and format checks still work")

// Confine puts a command under the session's sandbox. It returns the command to run and extra environment; nil argv means
// run it as it is. An error means it must not run.
type Confine func(argv []string) ([]string, []string, error)

// Engine runs conversions through LibreOffice.
type Engine struct {
	// Soffice is the binary; "" looks for it.
	Soffice string
	// Dir holds the private profile and temporary copies.
	Dir string
	// Confine may be nil: no sandbox applies.
	Confine Confine
	// Blocked, when set, says why the engine must not start here, whatever is installed.
	Blocked string
}

// One profile serves one process at a time.
var engineLocks sync.Map

// FindSoffice looks for LibreOffice; "" when there is none. Tests replace it, so that none of them starts the real one
// unless asked to.
var FindSoffice = findSoffice

func findSoffice() string {
	for _, n := range []string{"soffice", "libreoffice"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{"/Applications/LibreOffice.app/Contents/MacOS/soffice"}
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, "Applications/LibreOffice.app/Contents/MacOS/soffice"))
		}
	case "windows":
		candidates = []string{`C:\Program Files\LibreOffice\program\soffice.exe`, `C:\Program Files (x86)\LibreOffice\program\soffice.exe`}
	default:
		candidates = []string{"/usr/bin/soffice", "/usr/lib/libreoffice/program/soffice", "/opt/libreoffice/program/soffice", "/snap/bin/libreoffice"}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func (e *Engine) binary() string {
	if e.Soffice != "" {
		return e.Soffice
	}
	return FindSoffice()
}

// Available reports whether conversions can run.
func (e *Engine) Available() bool { return e.Unavailable() == nil }

// Unavailable says why conversions cannot run; nil when they can.
func (e *Engine) Unavailable() error {
	if e.Blocked != "" {
		return errors.New("not available: " + e.Blocked)
	}
	if e.binary() == "" {
		return ErrNoEngine
	}
	return nil
}

// engineSettings recalculates on load always, runs no macros and updates no links: a document must not reach out while
// it is being checked.
const engineSettings = `<?xml version="1.0" encoding="UTF-8"?>
<oor:items xmlns:oor="http://openoffice.org/2001/registry" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
<item oor:path="/org.openoffice.Office.Calc/Formula/Load"><prop oor:name="OOXMLRecalcMode" oor:op="fuse"><value>0</value></prop></item>
<item oor:path="/org.openoffice.Office.Calc/Formula/Load"><prop oor:name="ODFRecalcMode" oor:op="fuse"><value>0</value></prop></item>
<item oor:path="/org.openoffice.Office.Common/Security/Scripting"><prop oor:name="MacroSecurityLevel" oor:op="fuse"><value>3</value></prop></item>
<item oor:path="/org.openoffice.Office.Common/Security/Scripting"><prop oor:name="DisableMacrosExecution" oor:op="fuse"><value>true</value></prop></item>
<item oor:path="/org.openoffice.Office.Common/Security/Scripting"><prop oor:name="BlockUntrustedRefererLinks" oor:op="fuse"><value>true</value></prop></item>
<item oor:path="/org.openoffice.Office.Calc/Content/Update"><prop oor:name="Link" oor:op="fuse"><value>2</value></prop></item>
<item oor:path="/org.openoffice.Office.Writer/Content/Update"><prop oor:name="Link" oor:op="fuse"><value>2</value></prop></item>
</oor:items>
`

// Convert runs a copy of data through LibreOffice and returns the output file.
func (e *Engine) Convert(ctx context.Context, name string, data []byte, filter, outExt string) ([]byte, error) {
	if err := e.Unavailable(); err != nil {
		return nil, err
	}
	bin := e.binary()
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return nil, err
	}
	profile := filepath.Join(e.Dir, "engine-profile")
	lk, _ := engineLocks.LoadOrStore(profile, &sync.Mutex{})
	mu := lk.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(filepath.Join(profile, "user"), 0o700); err != nil {
		return nil, err
	}
	settings := filepath.Join(profile, "user", "registrymodifications.xcu")
	if _, err := os.Stat(settings); err != nil {
		if err := os.WriteFile(settings, []byte(engineSettings), 0o600); err != nil {
			return nil, err
		}
	}
	work, err := os.MkdirTemp(e.Dir, "engine-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	inDir, outDir := filepath.Join(work, "in"), filepath.Join(work, "out")
	if err := os.MkdirAll(inDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, err
	}
	base := "document" + strings.ToLower(filepath.Ext(name))
	in := filepath.Join(inDir, base)
	if err := os.WriteFile(in, data, 0o600); err != nil {
		return nil, err
	}
	argv := []string{bin, "--headless", "--norestore", "--nolockcheck", "--nologo", "--nodefault", "-env:UserInstallation=" + fileURL(profile), "--convert-to", filter, "--outdir", outDir, in}
	var extraEnv []string
	if e.Confine != nil {
		wrapped, env, err := e.Confine(argv)
		if err != nil {
			return nil, fmt.Errorf("the engine must run under the sandbox, which refused it: %w", err)
		}
		if wrapped != nil {
			argv, extraEnv = wrapped, env
		}
	}
	ctx, cancel := context.WithTimeout(ctx, EngineTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Env = append(cmd.Env, "HOME="+profile, "SAL_USE_VCLPLUGIN=svp")
	setEngineProcess(cmd)
	cmd.WaitDelay = 5 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("the engine did not finish within %s", EngineTimeout)
	}
	result := filepath.Join(outDir, "document"+outExt)
	b, readErr := os.ReadFile(result)
	if readErr != nil {
		tail := strings.TrimSpace(out.String())
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		if runErr != nil {
			return nil, fmt.Errorf("the engine failed: %v: %s", runErr, tail)
		}
		return nil, fmt.Errorf("the engine could not convert the file: %s", tail)
	}
	return b, nil
}

// ToPDF renders an Office file to PDF through the engine.
func (e *Engine) ToPDF(ctx context.Context, name string, data []byte) ([]byte, error) {
	return e.Convert(ctx, name, data, "pdf", ".pdf")
}

// Recalculate compares a workbook's stored formula results with what the engine computes from scratch. A difference
// is a finding: either the file is stale or the engine and Excel disagree, which a person must look at.
func (e *Engine) Recalculate(ctx context.Context, name string, data []byte) ([]Finding, error) {
	out, err := e.Convert(ctx, "workbook.xlsx", data, "xlsx:Calc MS Excel 2007 XML", ".xlsx")
	if err != nil {
		return nil, err
	}
	od, err := openXLSX(data)
	if err != nil {
		return nil, err
	}
	nd, err := openXLSX(out)
	if err != nil {
		return nil, fmt.Errorf("the engine's output does not read: %v", err)
	}
	orig, calc := od.(*xlsxDoc), nd.(*xlsxDoc)
	var fs []Finding
	for _, s := range orig.sheets {
		ns := calc.byName[s.name]
		if ns == nil {
			fs = append(fs, Finding{SeverityUnverified, "xlsx:recalc-sheet:" + s.name, fmt.Sprintf("the engine's copy has no sheet %q", s.name)})
			continue
		}
		for _, c := range s.order {
			if c.formula == "" {
				continue
			}
			addr := s.name + "!" + c.ref()
			nc := ns.cells[[2]int{c.col, c.row}]
			if nc == nil {
				continue
			}
			was, now := orig.value(c), calc.value(nc)
			if sameValue(was, now) {
				continue
			}
			sev := SeverityWarning
			fs = append(fs, Finding{sev, "xlsx:recalc:" + addr, fmt.Sprintf("%s (=%s) stores %s; recalculated from scratch it is %s", addr, c.formula, toText(was), toText(now))})
		}
	}
	return fs, nil
}

func sameValue(a, b fval) bool {
	if x, ok := a.(float64); ok {
		if y, ok := b.(float64); ok {
			return math.Abs(x-y) <= 1e-9*math.Max(1, math.Abs(x))
		}
	}
	return toText(a) == toText(b)
}

func fileURL(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + strings.NewReplacer(" ", "%20", "#", "%23", "?", "%3F").Replace(p)
}

// SlideToPage turns a slide address into its 1-based position, for rendering; 0 when there is no such slide.
func SlideToPage(doc Document, id string) int {
	d, ok := doc.(*pptxDoc)
	if !ok {
		return 0
	}
	s := d.byID[strings.TrimPrefix(id, "s")]
	if s == nil {
		return 0
	}
	// A hidden slide is left out of the export.
	page := 0
	for _, x := range d.slides {
		if !x.hidden {
			page++
		}
		if x == s {
			if x.hidden {
				return 0
			}
			return page
		}
	}
	return 0
}

// ParsePagesFor parses a page list against a count.
func ParsePagesFor(spec string, total int) ([]int, error) { return parsePages(spec, total) }

// PageText returns the text of a PDF's pages, as the fallback for a model that cannot see images.
func PageText(pdf []byte, pages []int) (string, error) {
	doc, err := openPDF(pdf)
	if err != nil {
		return "", err
	}
	d := doc.(*pdfDoc)
	d.measure()
	var parts []string
	for _, n := range pages {
		if n >= 1 && n <= len(d.pages) {
			parts = append(parts, "--- page "+strconv.Itoa(n)+" ---\n"+d.pages[n-1].text)
		}
	}
	return clip(strings.Join(parts, "\n")), nil
}
