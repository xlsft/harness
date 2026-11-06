package extensions

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	skillsv1 "github.com/xlsft/harness/packages/client/gen/harness/skills/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/skills/v1/skillsv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

// LabelExternal is the source label of what external search finds.
const LabelExternal = "skills.sh"

// SkillSearch is external skill search. The server holds the token and the cache; a client only asks it.
type SkillSearch interface {
	// Available says whether it can be used; reason is the server's when not.
	Available(ctx context.Context) (ok bool, reason string, err error)
	Search(ctx context.Context, query string, limit int) ([]*skillsv1.ExternalSkill, error)
	Curated(ctx context.Context, limit int) ([]*skillsv1.ExternalSkill, error)
	Snapshot(ctx context.Context, id string) (*skillsv1.GetSkillSnapshotResponse, error)
}

type skillSearchClient struct {
	rpc    skillsv1connect.SkillSearchServiceClient
	tokens rpc.Tokens
}

// NewSkillSearch asks the xlsft`s harness server at baseURL.
func NewSkillSearch(baseURL string, hc *http.Client, tokens rpc.Tokens) SkillSearch {
	return &skillSearchClient{rpc: skillsv1connect.NewSkillSearchServiceClient(hc, strings.TrimRight(baseURL, "/"), rpc.ClientOptions()...), tokens: tokens}
}

func signed[T any](ctx context.Context, c *skillSearchClient, msg *T) (*connect.Request[T], error) {
	req := connect.NewRequest(msg)
	if c.tokens != nil {
		token, err := c.tokens.Token(ctx)
		if err != nil {
			return nil, err
		}
		rpc.SetBearer(req.Header(), token)
	}
	return req, nil
}

func (c *skillSearchClient) Available(ctx context.Context) (bool, string, error) {
	req, err := signed(ctx, c, &skillsv1.GetSkillSearchStatusRequest{})
	if err != nil {
		return false, "", err
	}
	res, err := c.rpc.GetSkillSearchStatus(ctx, req)
	if err != nil {
		return false, "", err
	}
	return res.Msg.GetAvailable(), res.Msg.GetReason(), nil
}

func (c *skillSearchClient) Search(ctx context.Context, query string, limit int) ([]*skillsv1.ExternalSkill, error) {
	req, err := signed(ctx, c, &skillsv1.SearchSkillsRequest{Query: query, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	res, err := c.rpc.SearchSkills(ctx, req)
	if err != nil {
		return nil, err
	}
	return res.Msg.GetSkills(), nil
}

func (c *skillSearchClient) Curated(ctx context.Context, limit int) ([]*skillsv1.ExternalSkill, error) {
	req, err := signed(ctx, c, &skillsv1.ListCuratedSkillsRequest{Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	res, err := c.rpc.ListCuratedSkills(ctx, req)
	if err != nil {
		return nil, err
	}
	return res.Msg.GetSkills(), nil
}

func (c *skillSearchClient) Snapshot(ctx context.Context, id string) (*skillsv1.GetSkillSnapshotResponse, error) {
	req, err := signed(ctx, c, &skillsv1.GetSkillSnapshotRequest{Id: id})
	if err != nil {
		return nil, err
	}
	res, err := c.rpc.GetSkillSnapshot(ctx, req)
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}
