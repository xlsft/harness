package syncclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"

	"google.golang.org/protobuf/proto"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	libraryv1 "github.com/xlsft/harness/packages/client/gen/harness/library/v1"
	objectsv1 "github.com/xlsft/harness/packages/client/gen/harness/objects/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

// The library: the agent's way to the server's artifacts, Knowledge and issues (harness.library.v1).

// UploadArtifact puts bytes in the object store, kind artifact, and waits for it: the tool that publishes them answers
// only when they are there.
func (c *Client) UploadArtifact(ctx context.Context, name, mediaType string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	use := &objectsv1.ObjectUse{Kind: objectsv1.ObjectKind_OBJECT_KIND_ARTIFACT, Name: name, ContentType: mediaType}
	size := int64(len(data))
	start, err := c.StartUpload(ctx, &objectsv1.StartUploadRequest{Sha256: sha, Size: size, Use: use})
	if err != nil {
		return "", err
	}
	if start.GetAlreadyStored() {
		return sha, nil
	}
	chunk := start.GetChunkSize()
	if chunk <= 0 {
		return "", errors.New("the server gave no chunk size")
	}
	have := map[int32]bool{}
	for _, i := range start.GetReceivedChunks() {
		have[i] = true
	}
	r := bytes.NewReader(data)
	buf := make([]byte, chunk)
	for i := int32(0); int64(i)*chunk < size; i++ {
		if have[i] {
			continue
		}
		n, err := r.ReadAt(buf, int64(i)*chunk)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if _, err := c.UploadChunk(ctx, &objectsv1.UploadChunkRequest{UploadId: start.GetUploadId(), Index: i, Data: buf[:n]}); err != nil {
			return "", err
		}
	}
	if _, err := c.FinishUpload(ctx, &objectsv1.FinishUploadRequest{UploadId: start.GetUploadId(), Use: use}); err != nil {
		return "", err
	}
	return sha, nil
}

func (c *Client) PublishArtifact(ctx context.Context, req *libraryv1.PublishArtifactRequest) (*libraryv1.PublishArtifactResponse, error) {
	return unary(ctx, c, req, c.artifacts.PublishArtifact)
}

func (c *Client) ListKnowledgeDocuments(ctx context.Context, req *libraryv1.ListKnowledgeDocumentsRequest) (*libraryv1.ListKnowledgeDocumentsResponse, error) {
	return unary(ctx, c, req, c.knowledge.ListKnowledgeDocuments)
}

func (c *Client) GetKnowledgeDocument(ctx context.Context, req *libraryv1.GetKnowledgeDocumentRequest) (*libraryv1.GetKnowledgeDocumentResponse, error) {
	return unary(ctx, c, req, c.knowledge.GetKnowledgeDocument)
}

func (c *Client) SearchKnowledge(ctx context.Context, req *libraryv1.SearchKnowledgeRequest) (*libraryv1.SearchKnowledgeResponse, error) {
	return unary(ctx, c, req, c.knowledge.SearchKnowledge)
}

func (c *Client) ListProjects(ctx context.Context, req *libraryv1.ListProjectsRequest) (*libraryv1.ListProjectsResponse, error) {
	return unary(ctx, c, req, c.projects.ListProjects)
}

func (c *Client) EnsureDefaultProject(ctx context.Context, req *libraryv1.EnsureDefaultProjectRequest) (*libraryv1.EnsureDefaultProjectResponse, error) {
	return unary(ctx, c, req, c.projects.EnsureDefaultProject)
}

func (c *Client) ListBoards(ctx context.Context, req *libraryv1.ListBoardsRequest) (*libraryv1.ListBoardsResponse, error) {
	return unary(ctx, c, req, c.boards.ListBoards)
}

func (c *Client) GetBoard(ctx context.Context, req *libraryv1.GetBoardRequest) (*libraryv1.GetBoardResponse, error) {
	return unary(ctx, c, req, c.boards.GetBoard)
}

func (c *Client) CreateBoard(ctx context.Context, req *libraryv1.CreateBoardRequest) (*libraryv1.CreateBoardResponse, error) {
	return unary(ctx, c, req, c.boards.CreateBoard)
}

func (c *Client) ListCards(ctx context.Context, req *libraryv1.ListCardsRequest) (*libraryv1.ListCardsResponse, error) {
	return unary(ctx, c, req, c.boards.ListCards)
}

func (c *Client) GetCard(ctx context.Context, req *libraryv1.GetCardRequest) (*libraryv1.GetCardResponse, error) {
	return unary(ctx, c, req, c.boards.GetCard)
}

func (c *Client) CreateCard(ctx context.Context, req *libraryv1.CreateCardRequest) (*libraryv1.CreateCardResponse, error) {
	return unary(ctx, c, req, c.boards.CreateCard)
}

func (c *Client) UpdateCard(ctx context.Context, req *libraryv1.UpdateCardRequest) (*libraryv1.UpdateCardResponse, error) {
	return unary(ctx, c, req, c.boards.UpdateCard)
}

func (c *Client) MoveCard(ctx context.Context, req *libraryv1.MoveCardRequest) (*libraryv1.MoveCardResponse, error) {
	return unary(ctx, c, req, c.boards.MoveCard)
}

func (c *Client) DeleteCard(ctx context.Context, req *libraryv1.DeleteCardRequest) (*libraryv1.DeleteCardResponse, error) {
	return unary(ctx, c, req, c.boards.DeleteCard)
}

func (c *Client) RetryCardOutput(ctx context.Context, req *libraryv1.RetryCardOutputRequest) (*libraryv1.RetryCardOutputResponse, error) {
	return unary(ctx, c, req, c.boards.RetryCardOutput)
}

func (c *Client) LinkCard(ctx context.Context, req *libraryv1.LinkCardRequest) (*libraryv1.LinkCardResponse, error) {
	return unary(ctx, c, req, c.boards.LinkCard)
}

func (c *Client) RelateCard(ctx context.Context, req *libraryv1.RelateCardRequest) (*libraryv1.RelateCardResponse, error) {
	return unary(ctx, c, req, c.boards.RelateCard)
}

func (c *Client) CommentCard(ctx context.Context, req *libraryv1.CommentCardRequest) (*libraryv1.CommentCardResponse, error) {
	return unary(ctx, c, req, c.boards.CommentCard)
}

func (c *Client) PrepareCardSession(ctx context.Context, req *libraryv1.PrepareCardSessionRequest) (*libraryv1.PrepareCardSessionResponse, error) {
	return unary(ctx, c, req, c.boards.PrepareCardSession)
}

func (c *Client) LinkSessionPullRequest(ctx context.Context, req *libraryv1.LinkSessionPullRequestRequest) (*libraryv1.LinkSessionPullRequestResponse, error) {
	return unary(ctx, c, req, c.boards.LinkSessionPullRequest)
}

func (c *Client) ClaimDeviceWork(ctx context.Context, req *libraryv1.ClaimDeviceWorkRequest) (*libraryv1.ClaimDeviceWorkResponse, error) {
	return unary(ctx, c, req, c.boards.ClaimDeviceWork)
}

func (c *Client) ReportDeviceWork(ctx context.Context, req *libraryv1.ReportDeviceWorkRequest) (*libraryv1.ReportDeviceWorkResponse, error) {
	return unary(ctx, c, req, c.boards.ReportDeviceWork)
}

func (c *Client) ForgetDeviceConnection(ctx context.Context, req *libraryv1.ForgetDeviceConnectionRequest) (*libraryv1.ForgetDeviceConnectionResponse, error) {
	return unary(ctx, c, req, c.boards.ForgetDeviceConnection)
}

func (c *Client) ListKnowledgeSources(ctx context.Context, req *libraryv1.ListKnowledgeSourcesRequest) (*libraryv1.ListKnowledgeSourcesResponse, error) {
	return unary(ctx, c, req, c.knowledge.ListKnowledgeSources)
}

func (c *Client) BrowseKnowledgeSource(ctx context.Context, req *libraryv1.BrowseKnowledgeSourceRequest) (*libraryv1.BrowseKnowledgeSourceResponse, error) {
	return unary(ctx, c, req, c.knowledge.BrowseKnowledgeSource)
}

func (c *Client) SearchTranscripts(ctx context.Context, req *libraryv1.SearchTranscriptsRequest) (*libraryv1.SearchTranscriptsResponse, error) {
	return unary(ctx, c, req, c.transcripts.SearchTranscripts)
}

// AutoMemoryAPI is the part of SyncService auto memory needs (implemented by *Client).
type AutoMemoryAPI interface {
	GetDocument(ctx context.Context, req *syncv1.GetDocumentRequest) (*syncv1.GetDocumentResponse, error)
	PutDocument(ctx context.Context, req *syncv1.PutDocumentRequest) (*syncv1.PutDocumentResponse, error)
	ListChanges(ctx context.Context, req *syncv1.ListChangesRequest) (*syncv1.ListChangesResponse, error)
}

// AutoMemoryDocs keeps auto memory in the synced documents: scope project, kind memory, key "user". It implements
// memory.AutoDocs.
type AutoMemoryDocs struct {
	API AutoMemoryAPI
}

func autoMemoryRef(projectKey string) *syncv1.DocumentRef {
	return &syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_PROJECT, Kind: syncv1.DocumentKind_DOCUMENT_KIND_MEMORY, Key: "user", ProjectKey: projectKey}
}

// Load implements memory.AutoDocs.
func (d AutoMemoryDocs) Load(ctx context.Context, projectKey string) (string, map[string]string, int64, bool, error) {
	res, err := d.API.GetDocument(ctx, &syncv1.GetDocumentRequest{Selector: &syncv1.GetDocumentRequest_Ref{Ref: autoMemoryRef(projectKey)}})
	if err != nil {
		if IsNotFound(err) {
			return "", nil, 0, false, nil
		}
		return "", nil, 0, false, err
	}
	doc := res.GetDocument()
	files := map[string]string{}
	for _, f := range doc.GetFiles() {
		files[f.GetPath()] = string(f.GetContent())
	}
	return doc.GetContent(), files, doc.GetVersion(), true, nil
}

// Save implements memory.AutoDocs.
func (d AutoMemoryDocs) Save(ctx context.Context, projectKey, index string, files map[string]string, base int64) (int64, bool, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]*syncv1.DocumentFile, 0, len(names))
	for _, n := range names {
		out = append(out, &syncv1.DocumentFile{Path: n, Content: []byte(files[n])})
	}
	res, err := d.API.PutDocument(ctx, &syncv1.PutDocumentRequest{Ref: autoMemoryRef(projectKey), Content: index, Files: out, BaseVersion: proto.Int64(base), Source: "client:auto-memory"})
	if err != nil {
		return 0, false, err
	}
	if res.GetDocument() == nil {
		return 0, true, nil
	}
	return res.GetDocument().GetVersion(), false, nil
}

// Projects implements memory.AutoDocs.
func (d AutoMemoryDocs) Projects(ctx context.Context) ([]string, error) {
	var out []string
	token := ""
	for {
		res, err := d.API.ListChanges(ctx, &syncv1.ListChangesRequest{PageToken: token, Kinds: []syncv1.DocumentKind{syncv1.DocumentKind_DOCUMENT_KIND_MEMORY}})
		if err != nil {
			return nil, err
		}
		for _, ch := range res.GetChanges() {
			if doc := ch.GetDocument(); doc != nil && doc.GetScope() == commonv1.Scope_SCOPE_PROJECT {
				out = append(out, doc.GetProjectKey())
			}
		}
		if !res.GetHasMore() {
			return out, nil
		}
		token = res.GetNextPageToken()
	}
}
