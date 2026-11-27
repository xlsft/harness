package tracker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

// What tracker and knowledge connections check the same way: the address, the sign-in method and the options.

var optionKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// signInSpec is the part of a provider's capabilities that says what a sign-in needs.
type signInSpec struct {
	authMethods    []AuthMethod
	optionFields   []OptionField
	defaultBaseURL string
	validate       func(baseURL string, options map[string]string) error
}

// checkAddress normalizes an address and rejects one that is not a plain http(s) URL. The default is used for an empty one.
func checkAddress(raw, def string) (string, *url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		raw = def
	}
	base := NormalizeURL(raw)
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", nil, errors.New("the address must be an http(s) URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", nil, errors.New("the address must not carry credentials, a query or a fragment")
	}
	return base, u, nil
}

// CheckOptions keeps the options a provider declares, fills defaults and checks required and allowed values. Unknown keys are
// dropped, so a form can't store what nothing reads.
func CheckOptions(fields []OptionField, in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range fields {
		if !optionKey.MatchString(f.Key) {
			return nil, fmt.Errorf("option key %q is not valid", f.Key)
		}
		v := strings.TrimSpace(in[f.Key])
		if v == "" {
			v = f.Default
		}
		if v == "" {
			if f.Required {
				return nil, fmt.Errorf("%s is required", f.Label)
			}
			continue
		}
		if len(f.AllowedValues) > 0 && !slices.Contains(f.AllowedValues, v) {
			return nil, fmt.Errorf("%s must be one of: %s", f.Label, strings.Join(f.AllowedValues, ", "))
		}
		if len(v) > 500 {
			return nil, fmt.Errorf("%s is too long", f.Label)
		}
		out[f.Key] = v
	}
	return out, nil
}

// checkSignIn validates a connection's own sign-in against its provider.
func checkSignIn(spec signInSpec, baseURL *string, auth AuthMethod, login, clientID string, options *map[string]string) (*url.URL, error) {
	base, u, err := checkAddress(*baseURL, spec.defaultBaseURL)
	if err != nil {
		return nil, err
	}
	*baseURL = base
	if auth == "" || !slices.Contains(spec.authMethods, auth) {
		return nil, errors.New("choose how this connection signs in")
	}
	switch auth {
	case AuthPassword:
		if strings.TrimSpace(login) == "" {
			return nil, errors.New("the login is required for login and password")
		}
	case AuthEmailToken:
		if !strings.Contains(login, "@") {
			return nil, errors.New("the account email is required with an API token")
		}
	case AuthOAuth:
		if strings.TrimSpace(clientID) == "" {
			return nil, errors.New("the OAuth client id is required: an admin of the instance creates the client")
		}
	}
	opts, err := CheckOptions(spec.optionFields, *options)
	if err != nil {
		return nil, err
	}
	*options = opts
	if spec.validate != nil {
		if err := spec.validate(base, opts); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// needsSecret says whether a method signs in with a stored secret.
func needsSecret(m AuthMethod) bool { return m != AuthOAuth && m != AuthVCS }

// DeviceVCSToken asks gh or glab on this device for a token for host, for AuthVCS. The token goes to the caller only: never to a
// log, an argument list or the server.
func DeviceVCSToken(ctx context.Context, host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", errors.New("no host to sign in to")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	try := func(name string, args ...string) string {
		if _, err := exec.LookPath(name); err != nil {
			return ""
		}
		out, err := exec.CommandContext(ctx, name, args...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	// gh: https://cli.github.com/manual/gh_auth_token. glab: https://gitlab.com/gitlab-org/cli/-/blob/main/docs/source/config/get.md
	if strings.Contains(host, "gitlab") {
		if t := try("glab", "config", "get", "token", "--host", host); t != "" {
			return t, nil
		}
		if t := try("gh", "auth", "token", "--hostname", host); t != "" {
			return t, nil
		}
	} else {
		if t := try("gh", "auth", "token", "--hostname", host); t != "" {
			return t, nil
		}
		if t := try("glab", "config", "get", "token", "--host", host); t != "" {
			return t, nil
		}
	}
	return "", fmt.Errorf("%w: neither gh nor glab is signed in to %s on this device", ErrAuth, host)
}
