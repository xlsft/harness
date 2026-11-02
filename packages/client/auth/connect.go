package auth

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	authv1 "github.com/xlsft/harness/packages/client/gen/harness/auth/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/auth/v1/authv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

// ConnectService implements Service with harness.auth.v1.DeviceAuthService.
type ConnectService struct {
	rpc authv1connect.DeviceAuthServiceClient
}

var _ Service = (*ConnectService)(nil)

// NewConnectService creates the adapter. httpClient may be nil.
func NewConnectService(baseURL string, httpClient *http.Client) *ConnectService {
	if httpClient == nil {
		httpClient = rpc.HTTPClient(baseURL)
	}
	return &ConnectService{rpc: authv1connect.NewDeviceAuthServiceClient(httpClient, baseURL, rpc.ClientOptions()...)}
}

func userFrom(u *authv1.User) User {
	role := strings.ToLower(strings.TrimPrefix(u.GetRole().String(), "ROLE_"))
	if role == "unspecified" {
		role = ""
	}
	return User{ID: u.GetId(), Email: u.GetEmail(), Name: u.GetName(), Role: role}
}

func (s *ConnectService) RequestDeviceCode(ctx context.Context, info DeviceInfo) (*DeviceCode, error) {
	resp, err := s.rpc.StartDeviceAuth(ctx, connect.NewRequest(&authv1.StartDeviceAuthRequest{
		ClientName: info.ClientName, DeviceName: info.DeviceName, Os: info.OS, Arch: info.Arch, ClientVersion: config.Version,
	}))
	if err != nil {
		return nil, rpc.ToAPIError(err)
	}
	m := resp.Msg
	return &DeviceCode{DeviceCode: m.GetDeviceCode(), UserCode: m.GetUserCode(), VerificationURI: m.GetVerificationUri(),
		VerificationURIComplete: m.GetVerificationUriComplete(), ExpiresIn: int(m.GetExpiresInSeconds()), Interval: int(m.GetIntervalSeconds())}, nil
}

func (s *ConnectService) PollDeviceToken(ctx context.Context, deviceCode string) (*DeviceGrant, error) {
	resp, err := s.rpc.PollDeviceAuth(ctx, connect.NewRequest(&authv1.PollDeviceAuthRequest{DeviceCode: deviceCode}))
	if err != nil {
		return nil, rpc.ToAPIError(err)
	}
	switch resp.Msg.GetStatus() {
	case authv1.PollDeviceAuthResponse_STATUS_APPROVED:
		c := resp.Msg.GetCredentials()
		return &DeviceGrant{DeviceID: c.GetDeviceId(), DeviceToken: c.GetDeviceToken(), AccessToken: c.GetAccessToken(),
			AccessExpiresIn: int(c.GetAccessExpiresInSeconds()), User: userFrom(c.GetUser())}, nil
	case authv1.PollDeviceAuthResponse_STATUS_SLOW_DOWN:
		return nil, ErrSlowDown
	case authv1.PollDeviceAuthResponse_STATUS_DENIED:
		return nil, ErrAccessDenied
	case authv1.PollDeviceAuthResponse_STATUS_EXPIRED:
		return nil, ErrExpiredToken
	default:
		return nil, ErrAuthorizationPending
	}
}

func (s *ConnectService) Refresh(ctx context.Context, deviceToken string) (*AccessGrant, error) {
	resp, err := s.rpc.RefreshAccessToken(ctx, connect.NewRequest(&authv1.RefreshAccessTokenRequest{DeviceToken: deviceToken}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeUnauthenticated {
			return nil, ErrDeviceRevoked
		}
		return nil, rpc.ToAPIError(err)
	}
	return &AccessGrant{AccessToken: resp.Msg.GetAccessToken(), AccessExpiresIn: int(resp.Msg.GetAccessExpiresInSeconds())}, nil
}

func (s *ConnectService) Me(ctx context.Context, accessToken string) (*Me, error) {
	req := connect.NewRequest(&authv1.GetMeRequest{})
	rpc.SetBearer(req.Header(), accessToken)
	resp, err := s.rpc.GetMe(ctx, req)
	if err != nil {
		return nil, rpc.ToAPIError(err)
	}
	d := resp.Msg.GetDevice()
	return &Me{User: userFrom(resp.Msg.GetUser()), Device: Device{ID: d.GetId(), Name: d.GetName(), CreatedAt: d.GetCreatedAt(), LastSeenAt: d.GetLastSeenAt()}}, nil
}

func (s *ConnectService) Logout(ctx context.Context, accessToken string) error {
	req := connect.NewRequest(&authv1.LogoutRequest{})
	rpc.SetBearer(req.Header(), accessToken)
	_, err := s.rpc.Logout(ctx, req)
	return rpc.ToAPIError(err)
}
