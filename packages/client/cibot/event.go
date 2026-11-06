// Package cibot is `harness-client ci`: the PR review bot and the @mention trigger, as CI runs them. It reads what happened
// from the CI environment, runs one headless agent with read-only tools by default, and posts through gh or glab.
// docs/ci.md describes the setup; ci/ holds the GitHub Action and the GitLab CI template.
//
// Everything on the code host is untrusted. A mention counts only from someone with write access; the agent runs without
// the repository's settings, hooks and MCP servers, because a pull request can change those files.
package cibot

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/xlsft/harness/packages/client/vcs"
)

// Platform is the CI system the run is on.
type Platform string

const (
	GitHub Platform = "github"
	GitLab Platform = "gitlab"
)

// Comment is the comment that may call the bot.
type Comment struct {
	ID string
	// Thread is where a reply goes: the review comment (GitHub) or discussion (GitLab); empty is the conversation.
	Thread string
	Author string
	// Association is GitHub's author_association; GitLab's payload has none, so the host is asked.
	Association string
	Body        string
	Path        string
	Line        int
}

// Event is what the CI run is about.
type Event struct {
	Platform Platform
	Repo     vcs.Repo
	Number   int64
	// IsIssue: an issue, not a PR or MR.
	IsIssue     bool
	Title       string
	Description string
	HeadSHA     string
	Draft       bool
	// Comment is set for a comment event; a pull request event has none.
	Comment *Comment
}

// ErrNoEvent means the run was started by something the bot has no business with.
var ErrNoEvent = errors.New("this CI event is not about a pull request, merge request or issue comment")

// ReadEvent reads the event from the CI environment.
func ReadEvent(getenv func(string) string, readFile func(string) ([]byte, error)) (*Event, error) {
	switch {
	case getenv("GITHUB_ACTIONS") == "true":
		return readGitHub(getenv, readFile)
	case getenv("GITLAB_CI") == "true":
		return readGitLab(getenv, readFile)
	}
	return nil, errors.New("not on a CI runner this command knows: it reads GitHub Actions' or GitLab CI's environment")
}

type ghUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type ghComment struct {
	ID          int64  `json:"id"`
	Body        string `json:"body"`
	User        ghUser `json:"user"`
	Association string `json:"author_association"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	InReplyTo   int64  `json:"in_reply_to_id"`
}

type ghPayload struct {
	Action      string `json:"action"`
	PullRequest *struct {
		Number int64  `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Draft  bool   `json:"draft"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Issue *struct {
		Number      int64  `json:"number"`
		Title       string `json:"title"`
		Body        string `json:"body"`
		User        ghUser `json:"user"`
		Association string `json:"author_association"`
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	Comment *ghComment `json:"comment"`
}

func readGitHub(getenv func(string) string, readFile func(string) ([]byte, error)) (*Event, error) {
	host := "github.com"
	if u, err := url.Parse(getenv("GITHUB_SERVER_URL")); err == nil && u.Host != "" {
		host = strings.ToLower(u.Host)
	}
	path := getenv("GITHUB_REPOSITORY")
	if !strings.Contains(path, "/") {
		return nil, errors.New("GITHUB_REPOSITORY is not owner/repo")
	}
	repo := vcs.Repo{Key: strings.ToLower(host + "/" + path), Host: host, Path: path}
	data, err := readFile(getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return nil, fmt.Errorf("reading the GitHub event: %w", err)
	}
	var p ghPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("reading the GitHub event: %w", err)
	}
	ev := &Event{Platform: GitHub, Repo: repo}
	switch getenv("GITHUB_EVENT_NAME") {
	case "pull_request", "pull_request_target":
		if p.PullRequest == nil {
			return nil, ErrNoEvent
		}
		pr := p.PullRequest
		ev.Number, ev.Title, ev.Description, ev.HeadSHA, ev.Draft = pr.Number, pr.Title, pr.Body, pr.Head.SHA, pr.Draft
	case "pull_request_review_comment":
		if p.PullRequest == nil || p.Comment == nil || p.Action != "created" {
			return nil, ErrNoEvent
		}
		pr := p.PullRequest
		ev.Number, ev.Title, ev.Description, ev.HeadSHA, ev.Draft = pr.Number, pr.Title, pr.Body, pr.Head.SHA, pr.Draft
		thread := p.Comment.ID
		if p.Comment.InReplyTo != 0 {
			thread = p.Comment.InReplyTo
		}
		ev.Comment = &Comment{ID: strconv.FormatInt(p.Comment.ID, 10), Thread: strconv.FormatInt(thread, 10), Author: p.Comment.User.Login, Association: p.Comment.Association, Body: p.Comment.Body, Path: p.Comment.Path, Line: p.Comment.Line}
	case "issue_comment":
		if p.Issue == nil || p.Comment == nil || p.Action != "created" {
			return nil, ErrNoEvent
		}
		ev.Number, ev.Title, ev.Description, ev.IsIssue = p.Issue.Number, p.Issue.Title, p.Issue.Body, p.Issue.PullRequest == nil
		ev.Comment = &Comment{ID: strconv.FormatInt(p.Comment.ID, 10), Author: p.Comment.User.Login, Association: p.Comment.Association, Body: p.Comment.Body}
	case "issues":
		if p.Issue == nil || (p.Action != "opened" && p.Action != "edited") {
			return nil, ErrNoEvent
		}
		ev.Number, ev.Title, ev.Description, ev.IsIssue = p.Issue.Number, p.Issue.Title, p.Issue.Body, true
		ev.Comment = &Comment{Author: p.Issue.User.Login, Association: p.Issue.Association, Body: p.Issue.Body}
	default:
		return nil, ErrNoEvent
	}
	if ev.Number <= 0 {
		return nil, ErrNoEvent
	}
	return ev, nil
}

type glPayload struct {
	ObjectKind string `json:"object_kind"`
	User       struct {
		Username string `json:"username"`
	} `json:"user"`
	Attrs struct {
		ID           int64  `json:"id"`
		Note         string `json:"note"`
		System       bool   `json:"system"`
		Noteable     string `json:"noteable_type"`
		DiscussionID string `json:"discussion_id"`
		Position     *struct {
			NewPath string `json:"new_path"`
			NewLine int    `json:"new_line"`
		} `json:"position"`
	} `json:"object_attributes"`
	MergeRequest *struct {
		IID         int64  `json:"iid"`
		Title       string `json:"title"`
		Description string `json:"description"`
		LastCommit  struct {
			ID string `json:"id"`
		} `json:"last_commit"`
	} `json:"merge_request"`
	Issue *struct {
		IID         int64  `json:"iid"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"issue"`
}

func readGitLab(getenv func(string) string, readFile func(string) ([]byte, error)) (*Event, error) {
	host := getenv("CI_SERVER_HOST")
	if host == "" {
		if u, err := url.Parse(getenv("CI_SERVER_URL")); err == nil {
			host = u.Host
		}
	}
	path := getenv("CI_PROJECT_PATH")
	if host == "" || !strings.Contains(path, "/") {
		return nil, errors.New("CI_SERVER_HOST and CI_PROJECT_PATH do not name a project")
	}
	host = strings.ToLower(host)
	repo := vcs.Repo{Key: strings.ToLower(host + "/" + path), Host: host, Path: path}
	ev := &Event{Platform: GitLab, Repo: repo}

	// A note arrives as the webhook body of a pipeline trigger: GitLab keeps it in the file TRIGGER_PAYLOAD names.
	if file := getenv("TRIGGER_PAYLOAD"); file != "" {
		data, err := readFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading the trigger payload: %w", err)
		}
		var p glPayload
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("reading the trigger payload: %w", err)
		}
		if p.ObjectKind != "note" || p.Attrs.System {
			return nil, ErrNoEvent
		}
		switch {
		case p.Attrs.Noteable == "MergeRequest" && p.MergeRequest != nil:
			m := p.MergeRequest
			ev.Number, ev.Title, ev.Description, ev.HeadSHA = m.IID, m.Title, m.Description, m.LastCommit.ID
		case p.Attrs.Noteable == "Issue" && p.Issue != nil:
			i := p.Issue
			ev.Number, ev.Title, ev.Description, ev.IsIssue = i.IID, i.Title, i.Description, true
		default:
			return nil, ErrNoEvent
		}
		ev.Comment = &Comment{ID: strconv.FormatInt(p.Attrs.ID, 10), Thread: p.Attrs.DiscussionID, Author: p.User.Username, Body: p.Attrs.Note}
		if pos := p.Attrs.Position; pos != nil {
			ev.Comment.Path, ev.Comment.Line = pos.NewPath, pos.NewLine
		}
		return ev, nil
	}

	iid, err := strconv.ParseInt(getenv("CI_MERGE_REQUEST_IID"), 10, 64)
	if err != nil || iid <= 0 {
		return nil, ErrNoEvent
	}
	ev.Number = iid
	ev.Title, ev.Description, ev.HeadSHA = getenv("CI_MERGE_REQUEST_TITLE"), getenv("CI_MERGE_REQUEST_DESCRIPTION"), getenv("CI_COMMIT_SHA")
	ev.Draft = getenv("CI_MERGE_REQUEST_DRAFT") == "true"
	return ev, nil
}
