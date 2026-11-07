package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/simulator"
)

// The iOS Simulator (simulator.proto): the Simulator pane's RPCs, over the client's simulator.Manager, which the
// sessions' Simulator* tools share. Device-local: only a frontend on this machine reaches them, and the relay has no
// command for them. What the agent does to a device is published to its session as SimulatorActivity.

func (s *Server) simulators() *simulator.Manager {
	s.simOnce.Do(func() {
		if s.Simulators == nil {
			s.Simulators = &simulator.Manager{Logger: s.log()}
		}
		if s.Simulators.OnActivity == nil {
			s.Simulators.OnActivity = s.onSimulatorActivity
		}
	})
	return s.Simulators
}

func (s *Server) onSimulatorActivity(sessionID, udid, action string) {
	if ls := s.liveSession(sessionID); ls != nil {
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SimulatorActivity{SimulatorActivity: &agentv1.SimulatorActivity{Udid: udid, Action: action}}})
	}
}

func refuseRemoteSimulator(addr string) error {
	if localPeer(addr) {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("the iOS Simulator can only be used from this machine"))
}

func simulatorError(err error) error {
	var se *simulator.SimctlError
	switch {
	case errors.Is(err, simulator.ErrNoXcode), errors.Is(err, simulator.ErrNoInput):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.As(err, &se):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func simulatorDevice(d simulator.Device) *agentv1.SimulatorDevice {
	return &agentv1.SimulatorDevice{Udid: d.UDID, Name: d.Name, State: d.State, Runtime: d.Runtime, RuntimeName: d.RuntimeName, Available: d.Available}
}

// ListSimulators lists the simulators of this Mac, booted first.
func (s *Server) ListSimulators(ctx context.Context, req *connect.Request[agentv1.ListSimulatorsRequest]) (*connect.Response[agentv1.ListSimulatorsResponse], error) {
	if err := refuseRemoteSimulator(req.Peer().Addr); err != nil {
		return nil, err
	}
	m := s.simulators()
	if !m.Available(ctx) {
		return connect.NewResponse(&agentv1.ListSimulatorsResponse{UnavailableReason: simulator.ErrNoXcode.Error()}), nil
	}
	devs, err := m.List(ctx)
	if err != nil {
		return nil, simulatorError(err)
	}
	out := &agentv1.ListSimulatorsResponse{}
	for _, d := range devs {
		if d.Available {
			out.Devices = append(out.Devices, simulatorDevice(d))
		}
	}
	return connect.NewResponse(out), nil
}

// simulatorUDID checks that a UDID from the frontend names a known device, so it can only name a simulator of this Mac.
func (s *Server) simulatorUDID(ctx context.Context, udid string) (string, error) {
	udid = strings.TrimSpace(udid)
	if udid == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("udid is required"))
	}
	devs, err := s.simulators().List(ctx)
	if err != nil {
		return "", simulatorError(err)
	}
	for _, d := range devs {
		if strings.EqualFold(d.UDID, udid) {
			return d.UDID, nil
		}
	}
	return "", connect.NewError(connect.CodeNotFound, errors.New("no simulator "+udid))
}

// BootSimulator boots a device and waits for it.
func (s *Server) BootSimulator(ctx context.Context, req *connect.Request[agentv1.BootSimulatorRequest]) (*connect.Response[agentv1.BootSimulatorResponse], error) {
	if err := refuseRemoteSimulator(req.Peer().Addr); err != nil {
		return nil, err
	}
	udid, err := s.simulatorUDID(ctx, req.Msg.GetUdid())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	if err := s.simulators().Boot(ctx, udid); err != nil {
		return nil, simulatorError(err)
	}
	return connect.NewResponse(&agentv1.BootSimulatorResponse{}), nil
}

// ShutdownSimulator shuts a device down.
func (s *Server) ShutdownSimulator(ctx context.Context, req *connect.Request[agentv1.ShutdownSimulatorRequest]) (*connect.Response[agentv1.ShutdownSimulatorResponse], error) {
	if err := refuseRemoteSimulator(req.Peer().Addr); err != nil {
		return nil, err
	}
	udid, err := s.simulatorUDID(ctx, req.Msg.GetUdid())
	if err != nil {
		return nil, err
	}
	if err := s.simulators().Shutdown(ctx, udid); err != nil {
		return nil, simulatorError(err)
	}
	return connect.NewResponse(&agentv1.ShutdownSimulatorResponse{}), nil
}

// WatchSimulator streams the frames of a booted device until the frontend goes away.
func (s *Server) WatchSimulator(ctx context.Context, req *connect.Request[agentv1.WatchSimulatorRequest], stream *connect.ServerStream[agentv1.WatchSimulatorResponse]) error {
	if err := refuseRemoteSimulator(req.Peer().Addr); err != nil {
		return err
	}
	udid, err := s.simulatorUDID(ctx, req.Msg.GetUdid())
	if err != nil {
		return err
	}
	interval := time.Duration(req.Msg.GetIntervalMs()) * time.Millisecond
	if interval != 0 {
		interval = min(max(interval, 100*time.Millisecond), 2*time.Second)
	}
	err = s.simulators().Watch(ctx, udid, interval, func(f simulator.Frame) error {
		return stream.Send(&agentv1.WatchSimulatorResponse{Jpeg: f.JPEG, Width: int32(f.W), Height: int32(f.H)})
	})
	if ctx.Err() != nil {
		return nil
	}
	return simulatorError(err)
}

// SimulatorInput sends the pane's tap, swipe, text, keys or button to a device.
func (s *Server) SimulatorInput(ctx context.Context, req *connect.Request[agentv1.SimulatorInputRequest]) (*connect.Response[agentv1.SimulatorInputResponse], error) {
	if err := refuseRemoteSimulator(req.Peer().Addr); err != nil {
		return nil, err
	}
	udid, err := s.simulatorUDID(ctx, req.Msg.GetUdid())
	if err != nil {
		return nil, err
	}
	m := s.simulators()
	switch in := req.Msg.GetInput().(type) {
	case *agentv1.SimulatorInputRequest_Tap:
		err = m.Tap(ctx, udid, simulator.Point{X: in.Tap.GetX(), Y: in.Tap.GetY()}, int(min(max(in.Tap.GetDurationMs(), 0), 10000)))
	case *agentv1.SimulatorInputRequest_Swipe:
		sw := in.Swipe
		err = m.Swipe(ctx, udid, simulator.Point{X: sw.GetFromX(), Y: sw.GetFromY()}, simulator.Point{X: sw.GetToX(), Y: sw.GetToY()}, int(sw.GetDurationMs()))
	case *agentv1.SimulatorInputRequest_Text:
		if len(in.Text) > 10000 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("text is longer than 10000 bytes"))
		}
		err = m.Type(ctx, udid, in.Text)
	case *agentv1.SimulatorInputRequest_Keys:
		err = m.Keys(ctx, udid, in.Keys)
	case *agentv1.SimulatorInputRequest_Button:
		err = m.Button(ctx, udid, in.Button, 0)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no input"))
	}
	if err != nil {
		return nil, simulatorError(err)
	}
	return connect.NewResponse(&agentv1.SimulatorInputResponse{}), nil
}
