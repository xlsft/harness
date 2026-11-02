package connectgw_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	"github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

func req() *llm.Request {
	return &llm.Request{Model: "fake-model", MaxTokens: 100, Messages: []llm.Message{{Role: "user", Content: []llm.ContentBlock{llm.TextBlock("hi")}}}}
}

func TestStreamAccumulates(t *testing.T) {
	srv, url := fakegateway.Start(t, func(n int, r *llm.Request) fakegateway.Reply {
		return fakegateway.Reply{Events: fakegateway.Message("msg_1",
			fakegateway.Thinking("think", "sig"), fakegateway.Text("Hello there"),
			fakegateway.ToolUse("toolu_1", "Read", map[string]any{"file_path": "/a"}))}
	})
	gw := connectgw.New(url, nil, auth.StaticToken("hat_x"))
	var seen []string
	resp, _, err := gateway.Send(context.Background(), gw, req(), func(e llm.StreamEvent) { seen = append(seen, e.Type) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := resp.Message.Content
	if resp.ID != "msg_1" || resp.StopReason != "tool_use" || len(c) != 3 || c[0].Signature != "sig" || c[1].Text != "Hello there" || string(c[2].Input) != `{"file_path":"/a"}` {
		t.Fatalf("resp %+v", resp)
	}
	if resp.Harness.CostUSD != 0.001 || resp.Usage.OutputTokens != 20 {
		t.Fatalf("meta %+v %+v", resp.Harness, resp.Usage)
	}
	if seen[0] != llm.EventMessageStart || seen[len(seen)-1] != llm.EventMessageStop {
		t.Fatalf("events %v", seen)
	}
	if got := srv.Requests()[0]; got.Model != "fake-model" || got.Messages[0].Content[0].Text != "hi" {
		t.Fatalf("request %+v", got)
	}
}

func TestStreamRetriesOverloadedBeforeContent(t *testing.T) {
	_, url := fakegateway.Start(t, func(n int, r *llm.Request) fakegateway.Reply {
		if n == 0 {
			return fakegateway.Reply{Err: fakegateway.ErrOverloaded}
		}
		return fakegateway.Reply{Events: fakegateway.Message("msg_2", fakegateway.Text("ok"))}
	})
	gw := connectgw.New(url, nil, auth.StaticToken("hat_x"))
	retries := 0
	p := gateway.RetryPolicy{MaxRetries: 2, BaseDelay: time.Millisecond, OnRetry: func(int, time.Duration, error) { retries++ }}
	resp, _, err := gateway.Send(context.Background(), gw, req(), nil, &p)
	if err != nil || resp.Message.Text() != "ok" || retries != 1 {
		t.Fatalf("resp=%+v err=%v retries=%d", resp, err, retries)
	}
}

func TestStreamErrorMappingAndIdle(t *testing.T) {
	_, url := fakegateway.Start(t, func(n int, r *llm.Request) fakegateway.Reply {
		if r.Messages[0].Content[0].Text == "hold" {
			return fakegateway.Reply{Events: fakegateway.Partial("msg_h", false, fakegateway.Text("partial")), Hold: true}
		}
		return fakegateway.Reply{Events: fakegateway.Partial("msg_e", false, fakegateway.Text("x")), Err: fakegateway.ErrOverloaded}
	})
	gw := connectgw.New(url, nil, auth.StaticToken("hat_x"))
	_, acc, err := gateway.Send(context.Background(), gw, req(), nil, &gateway.RetryPolicy{})
	var ae *llm.APIError
	if !errors.As(err, &ae) || ae.Type != "overloaded" || ae.RetryAfterMs != 10 {
		t.Fatalf("want overloaded APIError, got %v", err)
	}
	if acc == nil || len(acc.CompletedBlocks()) != 1 {
		t.Fatal("partial content must be kept")
	}
	gw.IdleTimeout = 200 * time.Millisecond
	r := req()
	r.Messages[0].Content[0].Text = "hold"
	start := time.Now()
	_, _, err = gateway.Send(context.Background(), gw, r, nil, &gateway.RetryPolicy{})
	if !errors.Is(err, gateway.ErrIdleTimeout) || time.Since(start) > 3*time.Second {
		t.Fatalf("idle watchdog: %v after %s", err, time.Since(start))
	}
	// Cancellation propagates.
	ctx, cancel := context.WithCancel(context.Background())
	gw.IdleTimeout = time.Minute
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if _, _, err := gateway.Send(ctx, gw, r, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestCountTokens(t *testing.T) {
	_, url := fakegateway.Start(t, nil)
	gw := connectgw.New(url, nil, auth.StaticToken("hat_x"))
	n, err := gw.CountTokens(context.Background(), req())
	if err != nil || !n.Estimated {
		t.Fatalf("count: %+v %v", n, err)
	}
}
