package linear

import (
	"context"
	"fmt"
	"strings"

	"github.com/xlsft/harness/packages/client/tracker"
)

// Linear's documents as Knowledge: read live, never stored. A source's narrowing is a Linear project's name, written
// `project: Handbook` or just `Handbook`; documents have no tree, so the top level is the list. The server mirrors this in
// packages/core/src/tracker/linear.ts.

var _ tracker.KnowledgeProvider = (*Provider)(nil)

const docSelection = `id title content url updatedAt project { name }`

// maxDocuments bounds one listing: Linear pages by cursor, an offset is read through from the start.
const maxDocuments = 250

type apiDocument struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	URL       string `json:"url"`
	UpdatedAt string `json:"updatedAt"`
	Project   *struct {
		Name string `json:"name"`
	} `json:"project"`
}

func (d apiDocument) project() string {
	if d.Project == nil {
		return ""
	}
	return d.Project.Name
}

func (d apiDocument) toArticle() tracker.Article {
	return tracker.Article{ID: d.ID, Title: d.Title, URL: d.URL, Content: d.Content, Project: d.project(), UpdatedAt: d.UpdatedAt}
}

// DocumentProject is the project name a source's narrowing names; "" for every document.
func DocumentProject(base string) string {
	b := strings.TrimSpace(base)
	if len(b) >= 8 && strings.EqualFold(b[:8], "project:") {
		b = strings.TrimSpace(b[8:])
	}
	return strings.Trim(b, `"'`)
}

func inProject(d apiDocument, project string) bool {
	return project == "" || strings.EqualFold(strings.TrimSpace(d.project()), project)
}

// SearchArticles implements tracker.KnowledgeProvider with Linear's document search; the narrowing is applied to what it finds.
func (p *Provider) SearchArticles(ctx context.Context, query, base string, limit int) ([]tracker.Article, error) {
	limit = max(limit, 1)
	project := DocumentProject(base)
	var d struct {
		SearchDocuments struct {
			Nodes []apiDocument `json:"nodes"`
		} `json:"searchDocuments"`
	}
	// Ask for more when narrowing, since some of what comes back is left out.
	first := limit
	if project != "" {
		first = min(limit*4, 100)
	}
	if err := p.gql(ctx, `query($term: String!, $first: Int) { searchDocuments(term: $term, first: $first) { nodes { `+docSelection+` } } }`, map[string]any{"term": query, "first": first}, &d); err != nil {
		return nil, err
	}
	var out []tracker.Article
	for _, n := range d.SearchDocuments.Nodes {
		if inProject(n, project) && len(out) < limit {
			out = append(out, n.toArticle())
		}
	}
	return out, nil
}

// ListArticles implements tracker.KnowledgeProvider: the documents, most recently updated first. They have no children.
func (p *Provider) ListArticles(ctx context.Context, parentID, base string, page tracker.Page) ([]tracker.Article, bool, error) {
	if parentID != "" {
		return nil, false, nil
	}
	limit := page.Limit
	if limit <= 0 {
		limit = 100
	}
	want := min(page.Offset+limit+1, maxDocuments)
	vars := map[string]any{}
	params, filter := "$first: Int, $after: String", ""
	if project := DocumentProject(base); project != "" {
		vars["filter"] = map[string]any{"project": map[string]any{"name": map[string]any{"eqIgnoreCase": project}}}
		params, filter = params+", $filter: DocumentFilter", ", filter: $filter"
	}
	var all []apiDocument
	after := ""
	for len(all) < want {
		var d struct {
			Documents struct {
				Nodes    []apiDocument `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"documents"`
		}
		vars["first"] = min(want-len(all), 100)
		vars["after"] = nil
		if after != "" {
			vars["after"] = after
		}
		q := `query(` + params + `) { documents(first: $first, after: $after, orderBy: updatedAt` + filter + `) { nodes { ` + docSelection + ` } pageInfo { hasNextPage endCursor } } }`
		if err := p.gql(ctx, q, vars, &d); err != nil {
			return nil, false, err
		}
		all = append(all, d.Documents.Nodes...)
		if !d.Documents.PageInfo.HasNextPage || d.Documents.PageInfo.EndCursor == "" || len(d.Documents.Nodes) == 0 {
			break
		}
		after = d.Documents.PageInfo.EndCursor
	}
	if page.Offset >= len(all) {
		return nil, false, nil
	}
	end := min(page.Offset+limit, len(all))
	out := make([]tracker.Article, 0, end-page.Offset)
	for _, n := range all[page.Offset:end] {
		out = append(out, n.toArticle())
	}
	return out, len(all) > end, nil
}

// GetArticle implements tracker.KnowledgeProvider.
func (p *Provider) GetArticle(ctx context.Context, id string) (*tracker.Article, error) {
	var d struct {
		Document *apiDocument `json:"document"`
	}
	if err := p.gql(ctx, `query($id: String!) { document(id: $id) { `+docSelection+` } }`, map[string]any{"id": id}, &d); err != nil {
		return nil, err
	}
	if d.Document == nil {
		return nil, fmt.Errorf("%w: document %s", tracker.ErrNotFound, id)
	}
	a := d.Document.toArticle()
	return &a, nil
}
