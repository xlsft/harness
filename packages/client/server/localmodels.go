package server

// Local LLM providers (proto/harness/agent/v1/localmodels.proto): model servers on this machine (Ollama, LM
// Studio, ...) the client calls directly. Their configuration stays on this device (localmodels package).

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/localmodels"
)

func (s *Server) localModels() *localmodels.Registry {
	if s.LocalModels != nil {
		return s.LocalModels
	}
	return localmodels.Default()
}

// localCatalogModel finds a local model id that is not in a session's catalog (detected after it opened).
func (s *Server) localCatalogModel(ctx context.Context, id string) (llm.Model, bool, error) {
	if !localmodels.IsLocal(id) {
		return llm.Model{}, false, nil
	}
	m, err := s.localModels().Lookup(ctx, id)
	if err != nil {
		return llm.Model{}, false, localErr(err)
	}
	return localmodels.CatalogEntry(m), true, nil
}

var kindToProto = map[localmodels.Kind]agentv1.LocalProviderKind{
	localmodels.KindOllama:   agentv1.LocalProviderKind_LOCAL_PROVIDER_KIND_OLLAMA,
	localmodels.KindLMStudio: agentv1.LocalProviderKind_LOCAL_PROVIDER_KIND_LM_STUDIO,
	localmodels.KindLlamaCpp: agentv1.LocalProviderKind_LOCAL_PROVIDER_KIND_LLAMA_CPP,
	localmodels.KindMLX:      agentv1.LocalProviderKind_LOCAL_PROVIDER_KIND_MLX,
	localmodels.KindVLLM:     agentv1.LocalProviderKind_LOCAL_PROVIDER_KIND_VLLM,
	localmodels.KindJan:      agentv1.LocalProviderKind_LOCAL_PROVIDER_KIND_JAN,
	localmodels.KindCustom:   agentv1.LocalProviderKind_LOCAL_PROVIDER_KIND_CUSTOM,
}

func kindFromProto(k agentv1.LocalProviderKind) (localmodels.Kind, bool) {
	if k == agentv1.LocalProviderKind_LOCAL_PROVIDER_KIND_UNSPECIFIED {
		return localmodels.KindCustom, true
	}
	for kind, pk := range kindToProto {
		if pk == k {
			return kind, true
		}
	}
	return "", false
}

func localProviderToProto(p localmodels.Provider) *agentv1.LocalProvider {
	out := &agentv1.LocalProvider{
		Id: p.ID, Kind: kindToProto[p.Kind], Name: p.Name, BaseUrl: p.BaseURL, Enabled: p.Enabled, Builtin: p.Builtin,
		Reachable: p.Reachable, Version: p.Version, Error: p.Error, HasApiKey: p.HasAPIKey, ContextLength: int64(p.ContextLength),
	}
	if !p.CheckedAt.IsZero() {
		out.CheckedAt = p.CheckedAt.UTC().Format(time.RFC3339)
	}
	for _, m := range p.Models {
		out.Models = append(out.Models, &agentv1.LocalModel{
			Id: m.ID, Name: m.Name, DisplayName: m.DisplayName, ContextWindow: int64(m.ContextWindow), MaxContextWindow: int64(m.MaxContextWindow),
			Tools: m.Tools, Vision: m.Vision, Thinking: m.Thinking, SizeBytes: m.SizeBytes, Family: m.Family,
			ParameterSize: m.ParameterSize, Quantization: m.Quantization, ModifiedAt: m.ModifiedAt,
		})
	}
	return out
}

func providersToProto(ps []localmodels.Provider) []*agentv1.LocalProvider {
	out := make([]*agentv1.LocalProvider, 0, len(ps))
	for _, p := range ps {
		out = append(out, localProviderToProto(p))
	}
	return out
}

// localErr maps registry errors to Connect codes.
func localErr(err error) error {
	if err == nil {
		return nil
	}
	if localmodels.IsBuiltinError(err) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	var ae *llm.APIError
	if errors.As(err, &ae) {
		switch ae.Type {
		case "permission":
			return connect.NewError(connect.CodePermissionDenied, err)
		case "invalid_request":
			return connect.NewError(connect.CodeInvalidArgument, err)
		case "not_found":
			return connect.NewError(connect.CodeNotFound, err)
		case "authentication":
			return connect.NewError(connect.CodeUnauthenticated, err)
		case "upstream":
			return connect.NewError(connect.CodeUnavailable, err)
		}
	}
	if errors.Is(err, context.Canceled) {
		return connect.NewError(connect.CodeCanceled, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (s *Server) ListLocalProviders(ctx context.Context, req *connect.Request[agentv1.ListLocalProvidersRequest]) (*connect.Response[agentv1.ListLocalProvidersResponse], error) {
	r := s.localModels()
	ps := r.List(ctx, req.Msg.GetRefresh())
	return connect.NewResponse(&agentv1.ListLocalProvidersResponse{Providers: providersToProto(ps), DisabledByPolicy: r.PolicyDisabled()}), nil
}

func (s *Server) AddLocalProvider(ctx context.Context, req *connect.Request[agentv1.AddLocalProviderRequest]) (*connect.Response[agentv1.AddLocalProviderResponse], error) {
	m := req.Msg
	kind, ok := kindFromProto(m.GetKind())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown provider kind"))
	}
	p, err := s.localModels().Add(ctx, localmodels.AddInput{Kind: kind, Name: m.GetName(), BaseURL: m.GetBaseUrl(), APIKey: m.GetApiKey(), ContextLength: int(m.GetContextLength())})
	if err != nil {
		return nil, localErr(err)
	}
	return connect.NewResponse(&agentv1.AddLocalProviderResponse{Provider: localProviderToProto(p)}), nil
}

func (s *Server) UpdateLocalProvider(ctx context.Context, req *connect.Request[agentv1.UpdateLocalProviderRequest]) (*connect.Response[agentv1.UpdateLocalProviderResponse], error) {
	m := req.Msg
	in := localmodels.UpdateInput{Name: m.Name, BaseURL: m.BaseUrl, APIKey: m.ApiKey, Enabled: m.Enabled}
	if m.ContextLength != nil {
		n := int(m.GetContextLength())
		in.ContextLength = &n
	}
	p, err := s.localModels().Update(ctx, m.GetId(), in)
	if err != nil {
		return nil, localErr(err)
	}
	return connect.NewResponse(&agentv1.UpdateLocalProviderResponse{Provider: localProviderToProto(p)}), nil
}

func (s *Server) RemoveLocalProvider(ctx context.Context, req *connect.Request[agentv1.RemoveLocalProviderRequest]) (*connect.Response[agentv1.RemoveLocalProviderResponse], error) {
	if err := s.localModels().Remove(req.Msg.GetId()); err != nil {
		return nil, localErr(err)
	}
	return connect.NewResponse(&agentv1.RemoveLocalProviderResponse{}), nil
}

func (s *Server) RefreshLocalProviders(ctx context.Context, req *connect.Request[agentv1.RefreshLocalProvidersRequest]) (*connect.Response[agentv1.RefreshLocalProvidersResponse], error) {
	r := s.localModels()
	r.Refresh(ctx, req.Msg.GetId())
	ps := r.List(ctx, false)
	return connect.NewResponse(&agentv1.RefreshLocalProvidersResponse{Providers: providersToProto(ps), DisabledByPolicy: r.PolicyDisabled()}), nil
}

func (s *Server) PullOllamaModel(ctx context.Context, req *connect.Request[agentv1.PullOllamaModelRequest], stream *connect.ServerStream[agentv1.PullOllamaModelResponse]) error {
	err := s.localModels().PullOllamaModel(ctx, req.Msg.GetProviderId(), req.Msg.GetModel(), func(p localmodels.PullProgress) error {
		return stream.Send(&agentv1.PullOllamaModelResponse{Status: p.Status, Digest: p.Digest, Total: p.Total, Completed: p.Completed, Done: p.Done})
	})
	return localErr(err)
}

func (s *Server) DeleteOllamaModel(ctx context.Context, req *connect.Request[agentv1.DeleteOllamaModelRequest]) (*connect.Response[agentv1.DeleteOllamaModelResponse], error) {
	if err := s.localModels().DeleteOllamaModel(ctx, req.Msg.GetProviderId(), req.Msg.GetModel()); err != nil {
		return nil, localErr(err)
	}
	return connect.NewResponse(&agentv1.DeleteOllamaModelResponse{}), nil
}
