package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/tools"
)

// MaxOutputChars is the default cap of the text of one MCP tool result sent to the model.
const MaxOutputChars = 100_000

// maxImageBytes is the largest base64 image passed through (larger ones become a note).
const maxImageBytes = 5 * 1024 * 1024

// callResult is the subset of the MCP CallToolResult the client maps.
type callResult struct {
	Content           []contentItem   `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

type contentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	// resource_link
	URI  string `json:"uri,omitempty"`
	Name string `json:"name,omitempty"`
	// resource (embedded)
	Resource *struct {
		URI      string `json:"uri"`
		MimeType string `json:"mimeType,omitempty"`
		Text     string `json:"text,omitempty"`
		Blob     string `json:"blob,omitempty"`
	} `json:"resource,omitempty"`
}

var imageMediaTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// ResultFromJSON maps an MCP CallToolResult (exact upstream JSON) to a tool result: text and images
// become content blocks, other content kinds a short note; structuredContent is used when there is
// no other content. The text is capped at MaxOutputChars.
func ResultFromJSON(raw []byte) tools.Result {
	return resultFromJSON(raw, MaxOutputChars, "")
}

// resultFromJSON is ResultFromJSON with a limit; text past the limit is kept in a file under scratchDir (when set)
// so the model can read it.
func resultFromJSON(raw []byte, limit int, scratchDir string) tools.Result {
	var cr callResult
	if err := json.Unmarshal(raw, &cr); err != nil {
		return tools.ErrorResult("The MCP server returned an invalid result: %v", err)
	}
	var blocks []llm.ContentBlock
	var texts []string
	budget := limit
	addText := func(s string) {
		if s == "" {
			return
		}
		if budget <= 0 {
			return
		}
		if len(s) > budget {
			note := fmt.Sprintf("\n\n[output truncated: the MCP result exceeded %d characters]", limit)
			if path := keepFullOutput(scratchDir, s); path != "" {
				note = fmt.Sprintf("\n\n[output truncated: the MCP result exceeded %d characters. This item in full: %s]", limit, path)
			}
			s = truncate(s, budget) + note
		}
		budget -= len(s)
		texts = append(texts, s)
		blocks = append(blocks, llm.TextBlock(s))
	}
	for _, c := range cr.Content {
		switch c.Type {
		case "text":
			addText(c.Text)
		case "image":
			if imageMediaTypes[c.MimeType] && len(c.Data) <= maxImageBytes && c.Data != "" {
				blocks = append(blocks, llm.ContentBlock{Type: llm.BlockImage, Source: &llm.Source{Type: "base64", MediaType: c.MimeType, Data: c.Data}})
			} else {
				addText(fmt.Sprintf("[image omitted: %s, %d bytes of base64]", c.MimeType, len(c.Data)))
			}
		case "audio":
			addText(fmt.Sprintf("[audio omitted: %s]", c.MimeType))
		case "resource_link":
			addText(fmt.Sprintf("[resource link: %s %s]", c.Name, c.URI))
		case "resource":
			if c.Resource == nil {
				continue
			}
			if c.Resource.Text != "" {
				addText(c.Resource.Text)
			} else {
				addText(fmt.Sprintf("[binary resource omitted: %s %s]", c.Resource.URI, c.Resource.MimeType))
			}
		default:
			addText(fmt.Sprintf("[unsupported content type %q]", c.Type))
		}
	}
	if len(blocks) == 0 && len(cr.StructuredContent) > 0 && string(cr.StructuredContent) != "null" {
		addText(string(cr.StructuredContent))
	}
	if len(blocks) == 0 {
		addText("(no content)")
	}
	r := tools.Result{Content: blocks, IsError: cr.IsError}
	if cr.IsError {
		r.Structured = "Error: " + strings.Join(texts, "\n")
		r.Summary = "Error"
	} else {
		r.Structured = strings.Join(texts, "\n")
	}
	return r
}

// keepFullOutput writes an oversized result to the session's scratch directory; "" when that is not possible.
func keepFullOutput(dir, text string) string {
	if dir == "" {
		return ""
	}
	f, err := os.CreateTemp(dir, "mcp-output-*.txt")
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		return ""
	}
	return f.Name()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
