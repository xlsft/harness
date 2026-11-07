// Package images is the client's view of image generation (docs/design/images.md): a request to an image model and
// what comes back. The server's models go through harness.llm.v1.GatewayService.GenerateImages (gateway/connectgw);
// device-local models ("local:<provider>/<model>") through an OpenAI-compatible local server (package localmodels).
// The ImageGenerate tool (package tools) writes the files.
package images

import (
	"bytes"
	"context"
)

// MaxCount is the most images one request may ask for.
const MaxCount = 4

// Input is an image to edit or to take as a reference.
type Input struct {
	Data      []byte
	MediaType string
	Name      string
}

// Request is one generation.
type Request struct {
	// Model is an image model id: one of the catalog's image models, or a device-local "local:" id.
	Model   string
	Prompt  string
	Size    string
	Quality string
	Count   int
	Inputs  []Input
	// SessionID and AgentID attribute the usage.
	SessionID string
	AgentID   string
}

// Image is one generated image.
type Image struct {
	Data      []byte
	MediaType string
}

// Result is what a generation returned.
type Result struct {
	Images        []Image
	RevisedPrompt string
	Text          string
	CostUSD       float64
	Priced        bool
	// Model is the model that served the request.
	Model string
}

// Generator generates images.
type Generator interface {
	GenerateImages(ctx context.Context, req Request) (*Result, error)
}

// Sniff returns the media type of PNG, JPEG, WebP and GIF data from its first bytes, "" for anything else.
func Sniff(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0x89, 'P', 'N', 'G'}):
		return "image/png"
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "image/webp"
	case bytes.HasPrefix(b, []byte("GIF8")):
		return "image/gif"
	}
	return ""
}

// Extension is the usual file extension of an image media type (".png" for unknown ones).
func Extension(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ".png"
}
