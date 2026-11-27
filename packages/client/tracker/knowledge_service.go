package tracker

import (
	"context"
	"fmt"
	"strings"
	"sync"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	libraryv1 "github.com/xlsft/harness/packages/client/gen/harness/library/v1"
)

// Knowledge source connections as the RPCs show them, and Knowledge's merged view: the server's sources and this device's.

// KnowledgeCapabilitiesProto is every knowledge provider's capabilities.
func KnowledgeCapabilitiesProto() []*libraryv1.KnowledgeCapabilities {
	var out []*libraryv1.KnowledgeCapabilities
	for _, f := range KnowledgeFactories() {
		out = append(out, knowledgeCapabilitiesProto(f.Capabilities))
	}
	return out
}

func knowledgeCapabilitiesProto(c KnowledgeCapabilities) *libraryv1.KnowledgeCapabilities {
	return &libraryv1.KnowledgeCapabilities{
		Provider: c.Provider, DisplayName: c.DisplayName, Description: c.Description, AuthMethods: authMethodsProto(c.AuthMethods), OptionFields: optionFieldsProto(c.OptionFields),
		DocsUrl: c.DocsURL, Logo: c.Logo, BaseUrlPlaceholder: c.BaseURLPlaceholder, DefaultBaseUrl: c.DefaultBaseURL, AuthHints: authHintsProto(c.AuthMethods, c.AuthHints),
		BorrowFrom: c.BorrowFrom, QueryLanguage: c.QueryLanguage, QueryExample: c.QueryExample, Tree: c.Tree,
	}
}

// KnowledgeConnectionProto shows a knowledge connection; secrets stay out.
func (s *Service) KnowledgeConnectionProto(c KnowledgeConnection) *libraryv1.KnowledgeConnection {
	p := &libraryv1.KnowledgeConnection{
		Id: c.ID, Scope: libraryv1.TrackerConnectionScope_TRACKER_CONNECTION_SCOPE_DEVICE, Provider: c.Provider, Name: c.Name, BaseUrl: c.BaseURL, Auth: AuthProto(c.Auth),
		Login: c.Login, OauthClientId: c.OAuthClientID, HasSecret: s.Knowledge.HasSecret(c), Enabled: c.Enabled, Query: c.Query, Options: c.Options,
		Healthy: c.Healthy, LastError: c.LastError, LastCheckedAt: c.LastCheckedAt,
	}
	if c.Borrow != "" {
		p.Borrow = &libraryv1.TrackerConnectionRef{Scope: libraryv1.TrackerConnectionScope_TRACKER_CONNECTION_SCOPE_DEVICE, Id: c.Borrow}
		if t, err := s.Store.Get(c.Borrow); err == nil {
			p.BorrowName = t.Name
		}
	}
	if f, err := knowledgeFactoryOf(c.Provider); err == nil {
		p.Capabilities = knowledgeCapabilitiesProto(f.Capabilities)
	}
	return p
}

// KnowledgeInputFromProto reads a form.
func KnowledgeInputFromProto(c *libraryv1.KnowledgeConnectionInput) KnowledgeInput {
	return KnowledgeInput{
		KnowledgeConnection: KnowledgeConnection{
			ID: c.GetId(), Provider: c.GetProvider(), Name: strings.TrimSpace(c.GetName()), BaseURL: c.GetBaseUrl(), Auth: AuthFromProto(c.GetAuth()), Login: strings.TrimSpace(c.GetLogin()),
			OAuthClientID: strings.TrimSpace(c.GetOauthClientId()), Enabled: c.GetEnabled(), Query: c.GetQuery(), Options: c.GetOptions(), Borrow: strings.TrimSpace(c.GetBorrowConnectionId()),
		},
		Secret: c.Secret, OAuthClientSecret: c.OauthClientSecret,
	}
}

// ---- Knowledge's merged view ----

// documentID is how a device source's article is named: the same form the server uses, conn:<knowledge connection id>:<article id>.
func documentID(connectionID, articleID string) string {
	return "conn:" + connectionID + ":" + articleID
}

// ParseDocumentID splits a provider document's id. ok is false for an internal one. The connection part is a knowledge connection's
// id, or a tracker connection's in ids made before the split (KnowledgeStore.KnowledgeOf resolves both).
func ParseDocumentID(id string) (connectionID, articleID string, ok bool) {
	rest, found := strings.CutPrefix(id, "conn:")
	if !found {
		return "", "", false
	}
	connectionID, articleID, ok = strings.Cut(rest, ":")
	return connectionID, articleID, ok && connectionID != "" && articleID != ""
}

func deviceSource(c KnowledgeConnection) *libraryv1.KnowledgeSourceRef {
	return &libraryv1.KnowledgeSourceRef{Id: c.ID, Kind: libraryv1.KnowledgeSourceKind_KNOWLEDGE_SOURCE_KIND_PROVIDER, Scope: commonv1.Scope_SCOPE_USER, Name: c.Name, Provider: c.Provider}
}

func (s *Service) deviceSources() []KnowledgeConnection {
	list, err := s.Knowledge.List()
	if err != nil {
		return nil
	}
	var out []KnowledgeConnection
	for _, c := range list {
		if c.Enabled {
			out = append(out, c)
		}
	}
	return out
}

// Sources lists what this user can read, the server's and this device's. A server that can't be reached still leaves this device's.
func (s *Service) Sources(ctx context.Context) ([]*libraryv1.KnowledgeSource, error) {
	var out []*libraryv1.KnowledgeSource
	var serverErr error
	if s.Server != nil {
		res, err := s.Server.ListKnowledgeSources(ctx, &libraryv1.ListKnowledgeSourcesRequest{})
		serverErr = err
		out = res.GetSources()
	}
	for _, c := range s.deviceSources() {
		out = append(out, &libraryv1.KnowledgeSource{Ref: deviceSource(c)})
	}
	if serverErr != nil && len(out) == 0 {
		return nil, serverErr
	}
	return out, nil
}

type knowledgeList struct {
	hits []*libraryv1.KnowledgeHit
}

// alternate takes each source's list in turn: their scores can't be compared.
func alternate(lists []knowledgeList, limit int) []*libraryv1.KnowledgeHit {
	var out []*libraryv1.KnowledgeHit
	for i := 0; len(out) < limit; i++ {
		any := false
		for _, l := range lists {
			if i < len(l.hits) && len(out) < limit {
				out = append(out, l.hits[i])
				any = true
			}
		}
		if !any {
			break
		}
	}
	return out
}

// Search searches every source this user can read, concurrently. scope narrows; sourceIDs, when set, picks sources.
func (s *Service) Search(ctx context.Context, query string, limit int32, scope commonv1.Scope, sourceIDs []string) ([]*libraryv1.KnowledgeHit, []*libraryv1.KnowledgeWarning, error) {
	if limit <= 0 {
		limit = 8
	}
	limit = min(limit, 30)
	wanted := func(id string) bool {
		if len(sourceIDs) == 0 {
			return true
		}
		for _, x := range sourceIDs {
			if x == id {
				return true
			}
		}
		return false
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		lists    []knowledgeList
		warnings []*libraryv1.KnowledgeWarning
		fatal    error
	)
	if s.Server != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.Server.SearchKnowledge(ctx, &libraryv1.SearchKnowledgeRequest{Query: query, Limit: limit, Scope: scope})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fatal = err
				return
			}
			var hits []*libraryv1.KnowledgeHit
			for _, h := range res.GetHits() {
				if wanted(h.GetSource().GetId()) {
					hits = append(hits, h)
				}
			}
			lists = append(lists, knowledgeList{hits})
			warnings = append(warnings, res.GetWarnings()...)
		}()
	}
	if scope != commonv1.Scope_SCOPE_ORG {
		terms := FreeTextTerms(query)
		for _, c := range s.deviceSources() {
			if !wanted(c.ID) {
				continue
			}
			wg.Add(1)
			go func(c KnowledgeConnection) {
				defer wg.Done()
				articles, err := s.searchDevice(ctx, c, query, int(limit))
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					warnings = append(warnings, &libraryv1.KnowledgeWarning{Source: deviceSource(c), Message: err.Error()})
					return
				}
				var hits []*libraryv1.KnowledgeHit
				for _, a := range articles {
					hits = append(hits, &libraryv1.KnowledgeHit{DocumentId: documentID(c.ID, a.ID), Title: a.Title, Scope: commonv1.Scope_SCOPE_USER, Snippet: Snippet(a.Content, terms), Source: deviceSource(c), Url: a.URL})
				}
				lists = append(lists, knowledgeList{hits})
			}(c)
		}
	}
	wg.Wait()
	// The server failing is a warning only if a source of this device could still answer.
	if fatal != nil {
		if len(lists) == 0 {
			return nil, nil, fatal
		}
		warnings = append(warnings, &libraryv1.KnowledgeWarning{Source: &libraryv1.KnowledgeSourceRef{Id: "server", Name: "the server"}, Message: "The server could not be searched right now."})
	}
	return alternate(lists, int(limit)), warnings, nil
}

func (s *Service) searchDevice(ctx context.Context, c KnowledgeConnection, query string, limit int) ([]Article, error) {
	kp, err := s.Knowledge.Open(c)
	if err != nil {
		return nil, err
	}
	return kp.SearchArticles(ctx, query, c.Query, limit)
}

// deviceSource finds an enabled source of this device by a source or document id's connection part.
func (s *Service) deviceSource(id string) (KnowledgeConnection, bool) {
	c, ok := s.Knowledge.KnowledgeOf(id)
	if !ok || !c.Enabled {
		return KnowledgeConnection{}, false
	}
	return c, true
}

func articleProto(c KnowledgeConnection, a *Article) *libraryv1.KnowledgeDocument {
	return &libraryv1.KnowledgeDocument{
		Id: documentID(c.ID, a.ID), Scope: commonv1.Scope_SCOPE_USER, Title: a.Title, SourceName: a.Project, Size: int64(len(a.Content)), CreatedAt: a.UpdatedAt, UpdatedAt: a.UpdatedAt,
		Source: deviceSource(c), Url: a.URL, HasChildren: a.HasChildren,
	}
}

// Browse lists a source's documents.
func (s *Service) Browse(ctx context.Context, sourceID, parentID string, limit, offset int32) ([]*libraryv1.KnowledgeDocument, bool, error) {
	if c, ok := s.deviceSource(sourceID); ok {
		kp, err := s.Knowledge.Open(c)
		if err != nil {
			return nil, false, err
		}
		if limit <= 0 {
			limit = 100
		}
		list, hasMore, err := kp.ListArticles(ctx, parentID, c.Query, Page{Limit: int(min(limit, 200)), Offset: int(offset)})
		if err != nil {
			return nil, false, err
		}
		out := make([]*libraryv1.KnowledgeDocument, 0, len(list))
		for i := range list {
			out = append(out, articleProto(c, &list[i]))
		}
		return out, hasMore, nil
	}
	if s.Server == nil {
		return nil, false, fmt.Errorf("%w: source %s", ErrNotFound, sourceID)
	}
	res, err := s.Server.BrowseKnowledgeSource(ctx, &libraryv1.BrowseKnowledgeSourceRequest{SourceId: sourceID, ParentId: parentID, Limit: limit, Offset: offset})
	if err != nil {
		return nil, false, err
	}
	return res.GetDocuments(), res.GetHasMore(), nil
}

// Read reads a document from the right source.
func (s *Service) Read(ctx context.Context, documentID string) (*libraryv1.KnowledgeDocument, string, error) {
	if connID, articleID, ok := ParseDocumentID(documentID); ok {
		if c, local := s.deviceSource(connID); local {
			kp, err := s.Knowledge.Open(c)
			if err != nil {
				return nil, "", err
			}
			a, err := kp.GetArticle(ctx, articleID)
			if err != nil {
				return nil, "", err
			}
			return articleProto(c, a), a.Content, nil
		}
	}
	if s.Server == nil {
		return nil, "", fmt.Errorf("%w: document %s", ErrNotFound, documentID)
	}
	res, err := s.Server.GetKnowledgeDocument(ctx, &libraryv1.GetKnowledgeDocumentRequest{Id: documentID})
	if err != nil {
		return nil, "", err
	}
	return res.GetDocument(), res.GetContent(), nil
}
