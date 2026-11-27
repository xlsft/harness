package server

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/localmodels"
)

func TestImageModelIsASyncedPreference(t *testing.T) {
	ctx := context.Background()
	docs := &fakeDocs{}
	s := &Server{Preferences: docs}
	docs.write(`{"model":"big"}`)
	up, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{ImageModel: proto.String("img-std")}))
	if err != nil {
		t.Fatal(err)
	}
	if up.Msg.Preferences.GetImageModel() != "img-std" {
		t.Fatalf("after update: %q", up.Msg.Preferences.GetImageModel())
	}
	var stored map[string]any
	_ = json.Unmarshal([]byte(docs.doc.GetContent()), &stored)
	if stored["imageModel"] != "img-std" || stored["model"] != "big" {
		t.Fatalf("stored: %v", stored)
	}
	// A device-local model never goes into the synced document.
	_, err = s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{ImageModel: proto.String("local:lmstudio/sd")}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("local id accepted: %v", err)
	}
}

func TestDeviceImageModelStaysOnTheDevice(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	reg, err := localmodels.New(localmodels.Options{NoBuiltins: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := reg.Add(context.Background(), localmodels.AddInput{BaseURL: "http://127.0.0.1:9/v1"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{LocalModels: reg}
	id := localmodels.ModelID(p.ID, "sd-turbo")
	res, err := s.UpdateDeviceSettings(context.Background(), connect.NewRequest(&agentv1.UpdateDeviceSettingsRequest{ImageModel: proto.String(id)}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetSettings().GetImageModel() != id {
		t.Fatalf("settings: %v", res.Msg.GetSettings())
	}
	if got := readDeviceSettings().ImageModel; got != id {
		t.Fatalf("stored %q", got)
	}
	if _, err := s.UpdateDeviceSettings(context.Background(), connect.NewRequest(&agentv1.UpdateDeviceSettingsRequest{ImageModel: proto.String("local:nope/x")})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown provider accepted: %v", err)
	}
	if _, err := s.UpdateDeviceSettings(context.Background(), connect.NewRequest(&agentv1.UpdateDeviceSettingsRequest{ImageModel: proto.String("")})); err != nil {
		t.Fatal(err)
	}
	if got := readDeviceSettings().ImageModel; got != "" {
		t.Fatalf("not cleared: %q", got)
	}
}
