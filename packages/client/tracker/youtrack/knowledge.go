package youtrack

import (
	"github.com/xlsft/harness/packages/client/tracker"
)

// KnowledgeName is the YouTrack knowledge base as a knowledge provider. It reads the articles of an instance, usually with the
// sign-in of a YouTrack tracker connection (BorrowFrom), or with its own. The article methods are Provider's.
const KnowledgeName = "youtrack-kb"

func init() {
	tracker.RegisterKnowledge(KnowledgeName, tracker.KnowledgeFactory{
		Capabilities: KnowledgeCapabilities(),
		Open:         OpenKnowledge,
		OAuth: func(baseURL string) (tracker.OAuthExchange, error) {
			c, err := New(Options{BaseURL: baseURL, Token: "unused"})
			if err != nil {
				return nil, err
			}
			return oauthExchange{c}, nil
		},
	})
}

// KnowledgeCapabilities describes the knowledge provider.
func KnowledgeCapabilities() tracker.KnowledgeCapabilities {
	return tracker.KnowledgeCapabilities{
		Provider:    KnowledgeName,
		DisplayName: "YouTrack Knowledge Base",
		Description: "Articles of a YouTrack instance, read live.",
		AuthMethods: []tracker.AuthMethod{tracker.AuthToken, tracker.AuthOAuth, tracker.AuthPassword},
		// https://www.jetbrains.com/help/youtrack/server/knowledge-base.html
		DocsURL:            "https://www.jetbrains.com/help/youtrack/server/knowledge-base.html",
		Logo:               "youtrack",
		BorrowFrom:         []string{Name},
		QueryLanguage:      "YouTrack search",
		QueryExample:       "project: HELP",
		BaseURLPlaceholder: "https://youtrack.example.com",
		AuthHints:          authHints,
		Tree:               true,
	}
}

// OpenKnowledge opens the knowledge base over the same client as the tracker.
func OpenKnowledge(o tracker.KnowledgeOpenOptions) (tracker.KnowledgeProvider, error) {
	p, err := Open(tracker.OpenOptions{BaseURL: o.BaseURL, Auth: o.Auth, Login: o.Login, OAuthClientID: o.OAuthClientID, Credentials: o.Credentials, Options: o.Options, OnTokens: o.OnTokens})
	if err != nil {
		return nil, err
	}
	return p.(*Provider), nil
}
