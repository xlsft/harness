package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/doctor"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/permissions"
)

func TestRunDoctorUsesTheLivePolicy(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	s := &Server{}
	s.Base.ModePolicy = permissions.NewModePolicy()
	s.Base.ModePolicy.SetManaged(permissions.ManagedPolicy{MinClientVersion: "9.0.0", MaxClientVersion: "9.9.0"})
	s.DoctorOptions = func(context.Context) doctor.Options { return doctor.Options{ClientVersion: "0.3.0"} }
	res, err := s.RunDoctor(context.Background(), connect.NewRequest(&agentv1.RunDoctorRequest{SkipMcp: true}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.GetChecks()) != len(doctor.Order) || res.Msg.GetClientVersion() != "0.3.0" {
		t.Fatalf("%v", res.Msg)
	}
	for _, c := range res.Msg.GetChecks() {
		switch c.GetId() {
		case "version_policy":
			if c.GetStatus() != agentv1.DoctorStatus_DOCTOR_STATUS_FAIL {
				t.Errorf("below the minimum: %v", c)
			}
		case "mcp":
			if c.GetStatus() != agentv1.DoctorStatus_DOCTOR_STATUS_SKIP {
				t.Errorf("skip_mcp: %v", c)
			}
		case "signin":
			if c.GetStatus() != agentv1.DoctorStatus_DOCTOR_STATUS_FAIL {
				t.Errorf("no sign-in: %v", c)
			}
		}
	}
	if res.Msg.GetStatus() != agentv1.DoctorStatus_DOCTOR_STATUS_FAIL {
		t.Fatalf("overall: %v", res.Msg.GetStatus())
	}
}
