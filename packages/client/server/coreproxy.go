package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/gen/harness/auth/v1/authv1connect"
	"github.com/xlsft/harness/packages/client/gen/harness/llm/v1/llmv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

// CoreProxy forwards unary Connect calls of selected harness server services
// (harness.llm.v1.ProviderService: the settings screens' providers and
// classifier; harness.llm.v1.UsageService: the Usage tab's reports;
// harness.auth.v1.AdminService: user administration and LDAP, admins only;
// harness.llm.v1.ProviderLimitsService: provider budgets, GetProviderLimits also
// as AgentService.GetProviderLimits in limits.go),
// plus the account methods of harness.auth.v1.DeviceAuthService
// (coreProxyProcedures: the settings screen's profile, password and devices),
// to the server with this client's access token. Frontends talk
// to the client only and never hold server credentials; the server decides
// what the signed-in user may do, exactly as for its own calls.
//
// Only the listed services and procedures are forwarded, only unary Connect requests
// (POST, application/proto or application/json), with a bounded body. The
// frontend's Authorization header (the local agent token) and cookies are
// never forwarded: the request to the server is built from scratch.
type CoreProxy struct {
	// URL is the harness server base URL.
	URL    string
	HTTP   *http.Client
	Tokens rpc.Tokens
}

// coreProxyServices are the server services frontends may reach through the client.
var coreProxyServices = []string{llmv1connect.ProviderServiceName, llmv1connect.UsageServiceName, authv1connect.AdminServiceName, llmv1connect.ProviderLimitsServiceName}

// coreProxyProcedures are single methods of other server services frontends may
// reach. Never the device flow, token refresh or Logout: those manage this
// client's own credentials.
var coreProxyProcedures = []string{
	authv1connect.DeviceAuthServiceGetMeProcedure,
	authv1connect.DeviceAuthServiceUpdateMeProcedure,
	authv1connect.DeviceAuthServiceChangePasswordProcedure,
	authv1connect.DeviceAuthServiceListDevicesProcedure,
	authv1connect.DeviceAuthServiceRevokeDeviceProcedure,
}

const coreProxyMaxBody = 1 << 20

// forwardedRequestHeaders are copied from the frontend's request.
var forwardedRequestHeaders = []string{"Content-Type", "Connect-Protocol-Version", "Connect-Timeout-Ms"}

func (p *CoreProxy) register(mux *http.ServeMux) {
	for _, s := range coreProxyServices {
		mux.Handle("/"+s+"/", p)
	}
	for _, path := range coreProxyProcedures {
		mux.Handle(path, p)
	}
}

func (p *CoreProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ct := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if ct != "application/proto" && ct != "application/json" {
		http.Error(w, "only unary Connect requests are forwarded", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, coreProxyMaxBody+1))
	if err != nil {
		http.Error(w, "cannot read request", http.StatusBadRequest)
		return
	}
	if len(body) > coreProxyMaxBody {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	send := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.URL, "/")+r.URL.Path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for _, h := range forwardedRequestHeaders {
			if v := r.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
		req.Header.Set("User-Agent", config.UserAgent())
		rpc.SetBearer(req.Header, token)
		return p.HTTP.Do(req)
	}

	token, err := p.Tokens.Token(ctx)
	if err != nil {
		writeConnectError(w, http.StatusUnauthorized, "unauthenticated", "not signed in to the harness server")
		return
	}
	res, err := send(token)
	if err == nil && res.StatusCode == http.StatusUnauthorized {
		res.Body.Close()
		if token, err = p.Tokens.Refresh(ctx); err == nil {
			res, err = send(token)
		}
	}
	if err != nil {
		writeConnectError(w, http.StatusBadGateway, "unavailable", "harness server is unreachable")
		return
	}
	defer res.Body.Close()
	for k, vs := range res.Header {
		if k == "Content-Type" || strings.HasPrefix(k, "Connect-") || k == "Grpc-Status" {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(res.Body, 16<<20))
}

// writeConnectError answers in the Connect unary error format.
func writeConnectError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"code":"`+code+`","message":"`+msg+`"}`)
}
