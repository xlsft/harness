package sidechat

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// retry is the retry policy of side chat requests (the user is waiting and can ask again).
var retry = gateway.RetryPolicy{MaxRetries: 1, BaseDelay: time.Second, MaxDelay: 5 * time.Second}

// Answer is the outcome of Ask.
type Answer struct {
	// Text is the answer's text (what arrived, when stopped or failed).
	Text  string
	Usage *llm.Usage
}

// Ask sends req through the gateway and calls onDelta with each piece of the answer's text as it streams.
// When ctx is cancelled or the request fails, the text received so far is returned with the error.
func Ask(ctx context.Context, gw gateway.Gateway, req *llm.Request, onDelta func(string)) (Answer, error) {
	var streamed []byte
	var usage *llm.Usage
	handler := func(ev llm.StreamEvent) {
		switch ev.Type {
		case llm.EventContentBlockDelta:
			var d struct {
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			if json.Unmarshal(ev.Raw, &d) == nil && d.Delta.Type == "text_delta" && d.Delta.Text != "" {
				streamed = append(streamed, d.Delta.Text...)
				if onDelta != nil {
					onDelta(d.Delta.Text)
				}
			}
		}
	}
	resp, acc, err := gateway.Send(ctx, gw, req, handler, &retry)
	a := Answer{Text: string(streamed)}
	if err == nil {
		if t := resp.Message.Text(); t != "" {
			a.Text = t
		}
		u := resp.Usage
		usage = &u
	} else if acc != nil && acc.Started() {
		u := acc.Response().Usage
		if u != (llm.Usage{}) {
			usage = &u
		}
	}
	a.Usage = usage
	return a, err
}
