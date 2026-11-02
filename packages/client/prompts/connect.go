package prompts

import (
	"context"
	"encoding/json"
	"net/http"

	"connectrpc.com/connect"

	promptsv1 "github.com/xlsft/harness/packages/client/gen/harness/prompts/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/prompts/v1/promptsv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

// ConnectSource implements Source with harness.prompts.v1.PromptService.
type ConnectSource struct {
	rpc    promptsv1connect.PromptServiceClient
	tokens rpc.Tokens
}

var _ Source = (*ConnectSource)(nil)

// NewConnectSource creates the adapter. httpClient may be nil.
func NewConnectSource(baseURL string, httpClient *http.Client, tokens rpc.Tokens) *ConnectSource {
	if httpClient == nil {
		httpClient = rpc.HTTPClient(baseURL)
	}
	return &ConnectSource{rpc: promptsv1connect.NewPromptServiceClient(httpClient, baseURL, rpc.ClientOptions()...), tokens: tokens}
}

// Fetch implements Source. The ETag is the bundle version.
func (s *ConnectSource) Fetch(ctx context.Context, model, etag string) (*Bundle, string, bool, error) {
	resp, err := rpc.WithAuth(ctx, s.tokens, func(token string) (*connect.Response[promptsv1.GetBundleResponse], error) {
		req := connect.NewRequest(&promptsv1.GetBundleRequest{Model: model, IfNoneMatch: etag})
		rpc.SetBearer(req.Header(), token)
		return s.rpc.GetBundle(ctx, req)
	})
	if err != nil {
		return nil, "", false, err
	}
	if resp.Msg.GetNotModified() {
		return nil, etag, true, nil
	}
	b := FromProto(resp.Msg.GetBundle())
	return b, b.Version, false, nil
}

// FromProto converts a protobuf bundle.
func FromProto(p *promptsv1.PromptBundle) *Bundle {
	b := &Bundle{Version: p.GetVersion(), Model: p.GetModel(), Tools: map[string]ToolPrompt{}, Reminders: map[string]string{}}
	for _, s := range p.GetSystem() {
		id := s.GetId()
		if s.GetDynamicBoundary() {
			id = DynamicBoundary
		}
		b.System = append(b.System, Section{ID: id, Text: s.GetText()})
	}
	for name, t := range p.GetTools() {
		var schema json.RawMessage
		if t.GetInputSchemaJson() != "" {
			schema = json.RawMessage(t.GetInputSchemaJson())
		}
		b.Tools[name] = ToolPrompt{Description: t.GetDescription(), InputSchema: schema}
	}
	for k, v := range p.GetReminders() {
		b.Reminders[k] = v
	}
	if p.GetCompactionInstruction() != "" {
		b.Compaction = &Instruction{Instruction: p.GetCompactionInstruction()}
	}
	if p.GetTitleInstruction() != "" {
		b.Title = &Instruction{Instruction: p.GetTitleInstruction()}
	}
	for id, st := range p.GetOutputStyles() {
		if b.OutputStyles == nil {
			b.OutputStyles = map[string]OutputStyle{}
		}
		if st.GetId() != "" {
			id = st.GetId()
		}
		b.OutputStyles[id] = OutputStyle{ID: id, Name: st.GetName(), Description: st.GetDescription(), Text: st.GetText(),
			KeepCodingInstructions: st.GetKeepCodingInstructions()}
	}
	return b
}

// ToProto converts a bundle to protobuf (used by servers and test fakes).
func ToProto(b *Bundle) *promptsv1.PromptBundle {
	p := &promptsv1.PromptBundle{Version: b.Version, Model: b.Model, Tools: map[string]*promptsv1.ToolDefinition{}, Reminders: map[string]string{}}
	for _, s := range b.System {
		if s.ID == DynamicBoundary {
			p.System = append(p.System, &promptsv1.SystemSection{Id: s.ID, DynamicBoundary: true})
			continue
		}
		p.System = append(p.System, &promptsv1.SystemSection{Id: s.ID, Text: s.Text})
	}
	for n, t := range b.Tools {
		p.Tools[n] = &promptsv1.ToolDefinition{Description: t.Description, InputSchemaJson: string(t.InputSchema)}
	}
	for k, v := range b.Reminders {
		p.Reminders[k] = v
	}
	if b.Compaction != nil {
		p.CompactionInstruction = b.Compaction.Instruction
	}
	if b.Title != nil {
		p.TitleInstruction = b.Title.Instruction
	}
	for id, st := range b.OutputStyles {
		if p.OutputStyles == nil {
			p.OutputStyles = map[string]*promptsv1.OutputStyle{}
		}
		p.OutputStyles[id] = &promptsv1.OutputStyle{Id: id, Name: st.Name, Description: st.Description, Text: st.Text,
			KeepCodingInstructions: st.KeepCodingInstructions}
	}
	return p
}
