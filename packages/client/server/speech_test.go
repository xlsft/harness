package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	speechv1 "github.com/xlsft/harness/packages/client/gen/harness/speech/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/speech/v1/speechv1connect"
	"github.com/xlsft/harness/packages/client/speech"
)

// fakeSpeechCore is the server's SpeechService: one global STT (openrouter, WAV only) and TTS (openai) provider.
type fakeSpeechCore struct {
	speechv1connect.UnimplementedSpeechServiceHandler
	transcribed []*speechv1.TranscribeRequest
	synthesized []*speechv1.SynthesizeRequest
}

func (f *fakeSpeechCore) ListProviders(_ context.Context, req *connect.Request[speechv1.ListProvidersRequest]) (*connect.Response[speechv1.ListProvidersResponse], error) {
	return connect.NewResponse(&speechv1.ListProvidersResponse{CanManage: true, Providers: []*speechv1.SpeechProvider{
		{Id: "openrouter", Kind: speechv1.SpeechKind_SPEECH_KIND_STT, Location: speechv1.SpeechLocation_SPEECH_LOCATION_GLOBAL, Configured: true, Available: true, InputMimeTypes: []string{"audio/wav"}},
		{Id: "openai", Kind: speechv1.SpeechKind_SPEECH_KIND_TTS, Location: speechv1.SpeechLocation_SPEECH_LOCATION_GLOBAL, Configured: true, Available: true},
	}}), nil
}

func (f *fakeSpeechCore) Transcribe(_ context.Context, req *connect.Request[speechv1.TranscribeRequest]) (*connect.Response[speechv1.TranscribeResponse], error) {
	f.transcribed = append(f.transcribed, req.Msg)
	return connect.NewResponse(&speechv1.TranscribeResponse{Text: "from the server", ProviderId: req.Msg.ProviderId, Model: "m"}), nil
}

func (f *fakeSpeechCore) Synthesize(_ context.Context, req *connect.Request[speechv1.SynthesizeRequest], stream *connect.ServerStream[speechv1.SynthesizeResponse]) error {
	f.synthesized = append(f.synthesized, req.Msg)
	_ = stream.Send(&speechv1.SynthesizeResponse{Audio: []byte("mp3-1"), MimeType: "audio/mpeg"})
	_ = stream.Send(&speechv1.SynthesizeResponse{Audio: []byte("mp3-2")})
	return stream.Send(&speechv1.SynthesizeResponse{Done: true, Characters: int64(len(req.Msg.Text))})
}

func newSpeechServer(t *testing.T) (*Server, agentv1connect.AgentServiceClient, *fakeSpeechCore, *fakeDocs) {
	t.Helper()
	core := &fakeSpeechCore{}
	mux := http.NewServeMux()
	mux.Handle(speechv1connect.NewSpeechServiceHandler(core))
	coreSrv := httptest.NewServer(mux)
	t.Cleanup(coreSrv.Close)

	models := speech.NewModels(filepath.Join(t.TempDir(), "speech", "models"), nil, nil)
	local := &speech.Local{Models: models, GOOS: "linux", Exec: func(_ context.Context, bin string, args, _ []string) ([]byte, error) {
		if strings.HasSuffix(bin, "sherpa-onnx-offline") {
			return []byte(`{"text": "spoken locally"}`), nil
		}
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--output-filename="); ok {
				_ = os.WriteFile(v, speech.EncodeWAV(speech.PCM{Rate: 16000, Samples: make([]int16, 100_000)}), 0o600)
			}
		}
		return nil, nil
	}}
	// Install the runtime, a Parakeet model and a Piper voice (files only; Exec stands in for the tools).
	for _, key := range []string{speech.RuntimeKey, "parakeet/parakeet-tdt-0.6b-v3", "piper/en_US-lessac-medium"} {
		x, _ := models.Find(key)
		for _, f := range x.Required {
			p := filepath.Join(models.Path(x), filepath.FromSlash(f))
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte("x"), 0o755)
		}
	}
	docs := &fakeDocs{}
	s := New(bootstrap.Options{})
	t.Cleanup(s.Close)
	s.Preferences = docs
	s.SpeechLocal = local
	s.SpeechCore = speech.NewCore(coreSrv.URL, coreSrv.Client(), nil)
	agentSrv := httptest.NewServer(s.mux())
	t.Cleanup(agentSrv.Close)
	return s, agentv1connect.NewAgentServiceClient(agentSrv.Client(), agentSrv.URL), core, docs
}

func TestSpeechSettingsSyncedAndDevice(t *testing.T) {
	ctx := context.Background()
	s, c, _, docs := newSpeechServer(t)

	// Local engines belong to the device part, global providers to the synced part.
	if _, err := c.UpdateSpeechSettings(ctx, connect.NewRequest(&agentv1.UpdateSpeechSettingsRequest{SyncedStt: &agentv1.SttSettings{ProviderId: "parakeet"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("local in synced: %v", err)
	}
	if _, err := c.UpdateSpeechSettings(ctx, connect.NewRequest(&agentv1.UpdateSpeechSettingsRequest{DeviceTts: &agentv1.TtsSettings{ProviderId: "openai"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("global in device: %v", err)
	}
	if _, err := c.UpdateSpeechSettings(ctx, connect.NewRequest(&agentv1.UpdateSpeechSettingsRequest{DeviceStt: &agentv1.SttSettings{ProviderId: "piper"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("tts engine for stt: %v", err)
	}

	res, err := c.UpdateSpeechSettings(ctx, connect.NewRequest(&agentv1.UpdateSpeechSettingsRequest{
		SyncedStt: &agentv1.SttSettings{ProviderId: "openrouter", Language: "de"},
		SyncedTts: &agentv1.TtsSettings{ProviderId: "openai", Voice: "nova", Speed: 1.25},
	}))
	if err != nil {
		t.Fatal(err)
	}
	st := res.Msg.Settings
	if st.Stt.ProviderId != "openrouter" || st.Tts.Voice != "nova" || !st.SyncedAvailable || st.DeviceStt.ProviderId != "" {
		t.Fatalf("synced %+v", st)
	}
	var doc map[string]any
	_ = json.Unmarshal([]byte(docs.doc.GetContent()), &doc)
	if doc["speech"].(map[string]any)["tts"].(map[string]any)["voice"] != "nova" {
		t.Fatalf("doc %s", docs.doc.GetContent())
	}

	// The device choice overrides the synced one on this device and never reaches the server document.
	res, err = c.UpdateSpeechSettings(ctx, connect.NewRequest(&agentv1.UpdateSpeechSettingsRequest{DeviceStt: &agentv1.SttSettings{ProviderId: "parakeet", Model: "parakeet-tdt-0.6b-v3"}}))
	if err != nil {
		t.Fatal(err)
	}
	st = res.Msg.Settings
	if st.Stt.ProviderId != "parakeet" || st.SyncedStt.ProviderId != "openrouter" || st.Tts.ProviderId != "openai" {
		t.Fatalf("device override %+v", st)
	}
	if strings.Contains(docs.doc.GetContent(), "parakeet") {
		t.Fatalf("device choice synced: %s", docs.doc.GetContent())
	}
	b, err := os.ReadFile(s.deviceSettingsPath())
	if err != nil || !strings.Contains(string(b), "parakeet") {
		t.Fatalf("device file %v %s", err, b)
	}
	// A local engine written into the synced document by hand is ignored.
	docs.write(`{"speech":{"stt":{"provider":"whisper"},"tts":{"provider":"openai"}}}`)
	got, _ := c.GetSpeechSettings(ctx, connect.NewRequest(&agentv1.GetSpeechSettingsRequest{}))
	if got.Msg.Settings.SyncedStt.ProviderId != "" || got.Msg.Settings.SyncedTts.ProviderId != "openai" {
		t.Fatalf("stale synced %+v", got.Msg.Settings)
	}

	// Without a server: the device part still works, the synced part is refused.
	s.Preferences = nil
	if _, err := c.UpdateSpeechSettings(ctx, connect.NewRequest(&agentv1.UpdateSpeechSettingsRequest{SyncedTts: &agentv1.TtsSettings{ProviderId: "openai"}})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("synced offline: %v", err)
	}
	got, err = c.GetSpeechSettings(ctx, connect.NewRequest(&agentv1.GetSpeechSettingsRequest{}))
	if err != nil || got.Msg.Settings.SyncedAvailable || got.Msg.Settings.Stt.ProviderId != "parakeet" {
		t.Fatalf("offline %v %+v", err, got)
	}
}

func TestSpeechRouting(t *testing.T) {
	ctx := context.Background()
	_, c, core, _ := newSpeechServer(t)

	list, err := c.ListSpeechProviders(ctx, connect.NewRequest(&agentv1.ListSpeechProvidersRequest{}))
	if err != nil || list.Msg.ServerError != "" || !list.Msg.CanManageGlobal {
		t.Fatalf("list %v %+v", err, list)
	}
	var ids []string
	for _, p := range list.Msg.Providers {
		ids = append(ids, p.Id)
	}
	if strings.Join(ids, ",") != "parakeet,whisper,piper,openrouter,openai" {
		t.Fatalf("ids %v", ids)
	}
	if !list.Msg.Providers[0].Available || list.Msg.Providers[1].Available {
		t.Fatalf("availability %+v", list.Msg.Providers[:2])
	}

	wav := speech.EncodeWAV(speech.PCM{Rate: 44100, Samples: make([]int16, 4410)})
	// No provider chosen yet.
	if _, err := c.TranscribeAudio(ctx, connect.NewRequest(&agentv1.TranscribeAudioRequest{Audio: wav, MimeType: "audio/wav"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("none chosen: %v", err)
	}
	if _, err := c.TranscribeAudio(ctx, connect.NewRequest(&agentv1.TranscribeAudioRequest{Audio: wav, MimeType: "audio/wav", ProviderId: "browser"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("browser: %v", err)
	}
	// Local: never reaches the server.
	tr, err := c.TranscribeAudio(ctx, connect.NewRequest(&agentv1.TranscribeAudioRequest{Audio: wav, MimeType: "audio/wav", ProviderId: "parakeet"}))
	if err != nil || tr.Msg.Text != "spoken locally" || tr.Msg.Model != "parakeet-tdt-0.6b-v3" || len(core.transcribed) != 0 {
		t.Fatalf("local %v %+v", err, tr)
	}
	if _, err := c.TranscribeAudio(ctx, connect.NewRequest(&agentv1.TranscribeAudioRequest{Audio: wav, MimeType: "audio/wav", ProviderId: "whisper"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("no whisper model: %v", err)
	}
	// Global: forwarded with the effective settings.
	_, _ = c.UpdateSpeechSettings(ctx, connect.NewRequest(&agentv1.UpdateSpeechSettingsRequest{SyncedStt: &agentv1.SttSettings{ProviderId: "openrouter", Language: "fr"}}))
	tr, err = c.TranscribeAudio(ctx, connect.NewRequest(&agentv1.TranscribeAudioRequest{Audio: wav, MimeType: "audio/wav"}))
	if err != nil || tr.Msg.Text != "from the server" || core.transcribed[0].Language != "fr" || core.transcribed[0].ProviderId != "openrouter" {
		t.Fatalf("global %v %+v", err, tr)
	}

	// Read aloud: Markdown is cleaned up; global audio streams through.
	_, _ = c.UpdateSpeechSettings(ctx, connect.NewRequest(&agentv1.UpdateSpeechSettingsRequest{SyncedTts: &agentv1.TtsSettings{ProviderId: "openai", Voice: "nova", Speed: 1.5}}))
	stream, err := c.SynthesizeSpeech(ctx, connect.NewRequest(&agentv1.SynthesizeSpeechRequest{Text: "**Hello** `world`\n\n```sh\nrm -rf /\n```"}))
	if err != nil {
		t.Fatal(err)
	}
	var audio []byte
	var msgs []*agentv1.SynthesizeSpeechResponse
	for stream.Receive() {
		msgs = append(msgs, stream.Msg())
		audio = append(audio, stream.Msg().Audio...)
	}
	if stream.Err() != nil || string(audio) != "mp3-1mp3-2" || msgs[0].MimeType != "audio/mpeg" || msgs[0].ProviderId != "openai" || !msgs[len(msgs)-1].Done {
		t.Fatalf("global synth %v %q %+v", stream.Err(), audio, msgs)
	}
	sent := core.synthesized[0]
	if sent.Voice != "nova" || sent.Speed != 1.5 || strings.Contains(sent.Text, "rm -rf") || !strings.Contains(sent.Text, "Hello world.") {
		t.Fatalf("sent %+v", sent)
	}

	// The device's Piper voice overrides the synced provider here: one WAV in pieces.
	_, _ = c.UpdateSpeechSettings(ctx, connect.NewRequest(&agentv1.UpdateSpeechSettingsRequest{DeviceTts: &agentv1.TtsSettings{ProviderId: "piper"}}))
	stream, err = c.SynthesizeSpeech(ctx, connect.NewRequest(&agentv1.SynthesizeSpeechRequest{Text: "Hi there", PlainText: true}))
	if err != nil {
		t.Fatal(err)
	}
	audio, msgs = nil, nil
	for stream.Receive() {
		msgs = append(msgs, stream.Msg())
		audio = append(audio, stream.Msg().Audio...)
	}
	if stream.Err() != nil || msgs[0].MimeType != "audio/wav" || msgs[0].Voice != "en_US-lessac-medium" || len(msgs) < 4 || len(core.synthesized) != 1 {
		t.Fatalf("piper %v %d %+v", stream.Err(), len(msgs), msgs[0])
	}
	if p, err := speech.DecodeWAV(audio); err != nil || len(p.Samples) != 100_000 {
		t.Fatalf("wav %v", err)
	}
}

func TestSpeechModels(t *testing.T) {
	ctx := context.Background()
	_, c, _, _ := newSpeechServer(t)
	res, err := c.ListLocalSpeechModels(ctx, connect.NewRequest(&agentv1.ListLocalSpeechModelsRequest{Kind: speechv1.SpeechKind_SPEECH_KIND_STT}))
	if err != nil || len(res.Msg.Models) == 0 || res.Msg.ModelsDir == "" {
		t.Fatalf("list %v %+v", err, res)
	}
	installed := map[string]bool{}
	for _, m := range res.Msg.Models {
		installed[m.Id] = m.Installed
	}
	if !installed["parakeet/parakeet-tdt-0.6b-v3"] || installed["whisper/base"] {
		t.Fatalf("installed %v", installed)
	}
	// An installed model answers done at once.
	stream, err := c.DownloadSpeechModel(ctx, connect.NewRequest(&agentv1.DownloadSpeechModelRequest{Id: "parakeet/parakeet-tdt-0.6b-v3"}))
	if err != nil {
		t.Fatal(err)
	}
	var last *agentv1.DownloadSpeechModelResponse
	for stream.Receive() {
		last = stream.Msg()
	}
	if stream.Err() != nil || last == nil || !last.Done || !last.Model.Installed {
		t.Fatalf("download %v %+v", stream.Err(), last)
	}
	stream, _ = c.DownloadSpeechModel(ctx, connect.NewRequest(&agentv1.DownloadSpeechModelRequest{Id: "nope/x"}))
	for stream.Receive() {
	}
	if connect.CodeOf(stream.Err()) != connect.CodeNotFound {
		t.Fatalf("unknown: %v", stream.Err())
	}
	if _, err := c.DeleteSpeechModel(ctx, connect.NewRequest(&agentv1.DeleteSpeechModelRequest{Id: "parakeet/parakeet-tdt-0.6b-v3"})); err != nil {
		t.Fatal(err)
	}
	list, _ := c.ListSpeechProviders(ctx, connect.NewRequest(&agentv1.ListSpeechProvidersRequest{Kind: speechv1.SpeechKind_SPEECH_KIND_STT}))
	if list.Msg.Providers[0].Available {
		t.Fatal("parakeet still available")
	}
}
