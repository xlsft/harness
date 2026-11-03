package workspace

import "testing"

func TestRemoteWebURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/xlsft/harness.git":        "https://github.com/xlsft/harness",
		"https://github.com/xlsft/harness":            "https://github.com/xlsft/harness",
		"https://user:tok@GitHub.com/XLSFT/Harness/":  "https://github.com/XLSFT/Harness",
		"git@github.com:xlsft/harness.git":            "https://github.com/xlsft/harness",
		"github.com:xlsft/harness":                    "https://github.com/xlsft/harness",
		"ssh://git@github.com/xlsft/harness.git":      "https://github.com/xlsft/harness",
		"ssh://git@gitlab.example.com:2222/a/b/c.git": "https://gitlab.example.com/a/b/c",
		"git+ssh://git@host.xz/owner/repo":            "https://host.xz/owner/repo",
		"git://host.xz/path/to/repo.git":              "https://host.xz/path/to/repo",
		"https://git.example.com:8443/team/repo.git":  "https://git.example.com:8443/team/repo",
		"http://gitea.local:3000/me/tool.git":         "http://gitea.local:3000/me/tool",
		"  git@bitbucket.org:team/project.git\n":      "https://bitbucket.org/team/project",
		"/srv/git/repo.git":                           "",
		"../relative/repo":                            "",
		"file:///srv/git/repo.git":                    "",
		`C:\repos\x`:                                  "",
		"https://github.com/":                         "",
		"":                                            "",
		"ftp://example.com/repo.git":                  "",
		"git@github.com:":                             "",
	}
	for in, want := range cases {
		if got := RemoteWebURL(in); got != want {
			t.Errorf("RemoteWebURL(%q) = %q, want %q", in, got, want)
		}
	}
}
