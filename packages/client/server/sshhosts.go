package server

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	"github.com/xlsft/harness/packages/client/sshhost"
)

// SSH hosts (ssh_hosts.proto): sessions whose client runs on another machine. The work is in package sshhost; a host's client is
// reached directly by the frontend through the tunnel, so nothing here relays sessions. Device-local: nothing goes to the server.

var _ agentv1connect.SshHostServiceHandler = (*sshHostService)(nil)

type sshHostService struct {
	agentv1connect.UnimplementedSshHostServiceHandler
	s *Server

	once    sync.Once
	store   *sshhost.Store
	client  *sshhost.Client
	manager *sshhost.Manager
}

func (r *sshHostService) init() {
	r.once.Do(func() {
		dir := filepath.Join(config.Home(), "ssh")
		r.store = sshhost.NewStore(filepath.Join(dir, "hosts.json"))
		r.client = r.s.SSHClient
		if r.client == nil {
			r.client = &sshhost.Client{Dir: dir}
		}
		r.manager = sshhost.NewManager(sshhost.Options{
			Client: r.client, Store: r.store, Binaries: r.s.SSHBinaries, Version: config.UserAgent(),
			ServerURL: func() string { return r.s.Base.ServerURL },
		})
		context.AfterFunc(r.s.ctx, r.manager.Close)
	})
}

func hostProto(h sshhost.Host) *agentv1.SshHost {
	return &agentv1.SshHost{Id: h.ID, Name: h.Name, Destination: h.Destination, Port: int32(h.Port), IdentityFile: h.IdentityFile}
}

func hostError(err error) error {
	if errors.Is(err, sshhost.ErrNoHost) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	return err
}

func (r *sshHostService) ListSshHosts(context.Context, *connect.Request[agentv1.ListSshHostsRequest]) (*connect.Response[agentv1.ListSshHostsResponse], error) {
	r.init()
	hosts, err := r.store.List()
	if err != nil {
		return nil, err
	}
	out := &agentv1.ListSshHostsResponse{}
	for _, h := range hosts {
		out.Hosts = append(out.Hosts, hostProto(h))
	}
	return connect.NewResponse(out), nil
}

func (r *sshHostService) SaveSshHost(_ context.Context, req *connect.Request[agentv1.SaveSshHostRequest]) (*connect.Response[agentv1.SaveSshHostResponse], error) {
	r.init()
	m := req.Msg.GetHost()
	h := sshhost.Host{ID: m.GetId(), Name: m.GetName(), Destination: m.GetDestination(), Port: int(m.GetPort()), IdentityFile: m.GetIdentityFile()}
	if err := h.Validate(); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	saved, err := r.store.Save(h)
	if err != nil {
		return nil, hostError(err)
	}
	return connect.NewResponse(&agentv1.SaveSshHostResponse{Host: hostProto(saved)}), nil
}

func (r *sshHostService) DeleteSshHost(_ context.Context, req *connect.Request[agentv1.DeleteSshHostRequest]) (*connect.Response[agentv1.DeleteSshHostResponse], error) {
	r.init()
	r.manager.Disconnect(req.Msg.GetId())
	if err := r.store.Delete(req.Msg.GetId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.DeleteSshHostResponse{}), nil
}

func (r *sshHostService) CheckSshHostKey(ctx context.Context, req *connect.Request[agentv1.CheckSshHostKeyRequest]) (*connect.Response[agentv1.CheckSshHostKeyResponse], error) {
	r.init()
	h, err := r.store.Get(req.Msg.GetId())
	if err != nil {
		return nil, hostError(err)
	}
	check, err := r.client.CheckKey(ctx, h)
	if err != nil {
		return nil, err
	}
	out := &agentv1.CheckSshHostKeyResponse{Fingerprints: check.Fingerprints, Fingerprint: check.Fingerprint, Error: check.Err}
	switch check.Status {
	case sshhost.KeyVerified:
		out.Status = agentv1.SshHostKeyStatus_SSH_HOST_KEY_STATUS_VERIFIED
	case sshhost.KeyUnknown:
		out.Status = agentv1.SshHostKeyStatus_SSH_HOST_KEY_STATUS_UNKNOWN
	case sshhost.KeyChanged:
		out.Status = agentv1.SshHostKeyStatus_SSH_HOST_KEY_STATUS_CHANGED
	case sshhost.KeyUnreachable:
		out.Status = agentv1.SshHostKeyStatus_SSH_HOST_KEY_STATUS_UNREACHABLE
	}
	return connect.NewResponse(out), nil
}

func (r *sshHostService) TrustSshHostKey(ctx context.Context, req *connect.Request[agentv1.TrustSshHostKeyRequest]) (*connect.Response[agentv1.TrustSshHostKeyResponse], error) {
	r.init()
	h, err := r.store.Get(req.Msg.GetId())
	if err != nil {
		return nil, hostError(err)
	}
	if err := r.client.TrustKey(ctx, h, req.Msg.GetFingerprint()); err != nil {
		if errors.Is(err, sshhost.ErrKeyMoved) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return nil, err
	}
	return connect.NewResponse(&agentv1.TrustSshHostKeyResponse{}), nil
}

func (r *sshHostService) ForgetSshHostKey(ctx context.Context, req *connect.Request[agentv1.ForgetSshHostKeyRequest]) (*connect.Response[agentv1.ForgetSshHostKeyResponse], error) {
	r.init()
	h, err := r.store.Get(req.Msg.GetId())
	if err != nil {
		return nil, hostError(err)
	}
	if err := r.client.ForgetKey(ctx, h); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.ForgetSshHostKeyResponse{}), nil
}

func connProto(c *sshhost.Connection) *agentv1.SshConnection {
	return &agentv1.SshConnection{HostId: c.HostID, Url: c.URL, Token: c.Token, Platform: c.Platform, Email: c.Email}
}

func (r *sshHostService) ConnectSshHost(ctx context.Context, req *connect.Request[agentv1.ConnectSshHostRequest], stream *connect.ServerStream[agentv1.ConnectSshHostResponse]) error {
	r.init()
	var sendErr error
	conn, err := r.manager.Connect(ctx, req.Msg.GetId(), req.Msg.GetOrigin(), func(st sshhost.Stage) {
		if st.Name == "ready" {
			return // Sent below, with the connection.
		}
		if e := stream.Send(&agentv1.ConnectSshHostResponse{Stage: st.Name, Message: st.Message, VerificationUri: st.VerificationURI, UserCode: st.UserCode}); e != nil && sendErr == nil {
			sendErr = e
		}
	})
	if err != nil {
		switch {
		case errors.Is(err, sshhost.ErrNoHost):
			return connect.NewError(connect.CodeNotFound, err)
		case errors.Is(err, sshhost.ErrKeyNotVerified):
			return connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return connect.NewError(connect.CodeUnavailable, err)
	}
	if sendErr != nil {
		return sendErr
	}
	return stream.Send(&agentv1.ConnectSshHostResponse{Stage: "ready", Message: "Connected", Connection: connProto(conn)})
}

func (r *sshHostService) DisconnectSshHost(_ context.Context, req *connect.Request[agentv1.DisconnectSshHostRequest]) (*connect.Response[agentv1.DisconnectSshHostResponse], error) {
	r.init()
	r.manager.Disconnect(req.Msg.GetId())
	return connect.NewResponse(&agentv1.DisconnectSshHostResponse{}), nil
}

func (r *sshHostService) ListSshConnections(context.Context, *connect.Request[agentv1.ListSshConnectionsRequest]) (*connect.Response[agentv1.ListSshConnectionsResponse], error) {
	r.init()
	out := &agentv1.ListSshConnectionsResponse{}
	for _, c := range r.manager.Connections() {
		out.Connections = append(out.Connections, connProto(c))
	}
	return connect.NewResponse(out), nil
}
