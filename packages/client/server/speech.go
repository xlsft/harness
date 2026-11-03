package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	speechv1 "github.com/xlsft/harness/packages/client/gen/harness/speech/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/speech"
)

// Speech (proto/harness/agent/v1/speech.proto): the local engines of this machine (packages/client/speech) and the
// server's global providers (SpeechCore), and the user's choice between them in two parts:
//   - synced: the choice of a global provider, in the user's synced settings document (key "speech") on the
//     server, so every device of the user shares it;
//   - device: the choice of a local engine (or the frontend's "browser"), in HARNESS_HOME/speech/settings.json on
//     this machine only. When set it overrides the synced choice here.
// Local models and their state never leave this machine either.

// speechSettingsKey is the key of the synced speech choice in the user's settings document.
const speechSettingsKey = "speech"

// maxSpeechAudio bounds a recording to transcribe.
const maxSpeechAudio = 25 << 20

// speechChunk is the size of the audio pieces streamed from a local engine's WAV.
const speechChunk = 64 << 10

type sttChoice struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Language string `json:"language,omitempty"`
}

type ttsChoice struct {
	Provider string  `json:"provider,omitempty"`
	Model    string  `json:"model,omitempty"`
	Voice    string  `json:"voice,omitempty"`
	Speed    float64 `json:"speed,omitempty"`
}

type speechChoice struct {
	STT sttChoice `json:"stt"`
	TTS ttsChoice `json:"tts"`
}

// speechState holds the lazily created local engines and the device settings lock.
type speechState struct {
	once  sync.Once
	local *speech.Local
	// mu serializes device settings writes.
	mu sync.Mutex
	// accepts caches the audio types each global STT provider takes (from SpeechService.ListProviders).
	acceptsMu sync.Mutex
	accepts   map[string][]string
	acceptsAt time.Time
}

func (s *Server) speechLocal() *speech.Local {
	s.speech.once.Do(func() {
		s.speech.local = s.SpeechLocal
		if s.speech.local == nil {
			s.speech.local = &speech.Local{Models: speech.NewModels(filepath.Join(config.Home(), "speech", "models"), nil, nil)}
		}
	})
	return s.speech.local
}

func (s *Server) deviceSettingsPath() string {
	return filepath.Join(filepath.Dir(s.speechLocal().Models.Dir), "settings.json")
}

func (s *Server) readDeviceSpeech() speechChoice {
	var c speechChoice
	if b, err := os.ReadFile(s.deviceSettingsPath()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func (s *Server) writeDeviceSpeech(c speechChoice) error {
	p := s.deviceSettingsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// syncedSpeechOf reads the synced choice from a settings document (nil or invalid: none).
func syncedSpeechOf(doc *syncv1.Document) speechChoice {
	var raw map[string]json.RawMessage
	var c speechChoice
	if json.Unmarshal([]byte(doc.GetContent()), &raw) == nil {
		_ = json.Unmarshal(raw[speechSettingsKey], &c)
	}
	// A local engine never belongs to the synced part (written by hand or by an older client): ignore it.
	if !isGlobalProvider(c.STT.Provider) {
		c.STT = sttChoice{}
	}
	if !isGlobalProvider(c.TTS.Provider) {
		c.TTS = ttsChoice{}
	}
	return c
}

func isGlobalProvider(id string) bool {
	return id != "" && !speech.IsLocal(id) && id != speech.ProviderBrowser && id != speech.ProviderRuntime
}

func isDeviceProvider(id string) bool { return speech.IsLocal(id) || id == speech.ProviderBrowser }

func (s *Server) speechSettings(ctx context.Context) (*agentv1.SpeechSettings, error) {
	var synced speechChoice
	available := s.Preferences != nil
	if available {
		ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
		defer cancel()
		doc, err := s.userSettings(ctx)
		if err != nil {
			return nil, syncError(err)
		}
		synced = syncedSpeechOf(doc)
	}
	return buildSpeechSettings(synced, s.readDeviceSpeech(), available), nil
}

func buildSpeechSettings(synced, device speechChoice, available bool) *agentv1.SpeechSettings {
	stt := func(c sttChoice) *agentv1.SttSettings {
		return &agentv1.SttSettings{ProviderId: c.Provider, Model: c.Model, Language: c.Language}
	}
	tts := func(c ttsChoice) *agentv1.TtsSettings {
		return &agentv1.TtsSettings{ProviderId: c.Provider, Model: c.Model, Voice: c.Voice, Speed: c.Speed}
	}
	out := &agentv1.SpeechSettings{
		SyncedStt: stt(synced.STT), SyncedTts: tts(synced.TTS), DeviceStt: stt(device.STT), DeviceTts: tts(device.TTS),
		Stt: stt(synced.STT), Tts: tts(synced.TTS), SyncedAvailable: available,
	}
	if device.STT.Provider != "" {
		out.Stt = stt(device.STT)
	}
	if device.TTS.Provider != "" {
		out.Tts = tts(device.TTS)
	}
	return out
}

func (s *Server) GetSpeechSettings(ctx context.Context, _ *connect.Request[agentv1.GetSpeechSettingsRequest]) (*connect.Response[agentv1.GetSpeechSettingsResponse], error) {
	st, err := s.speechSettings(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.GetSpeechSettingsResponse{Settings: st}), nil
}

func sttFrom(m *agentv1.SttSettings) sttChoice {
	return sttChoice{Provider: strings.TrimSpace(m.GetProviderId()), Model: strings.TrimSpace(m.GetModel()), Language: strings.TrimSpace(m.GetLanguage())}
}

func ttsFrom(m *agentv1.TtsSettings) ttsChoice {
	speed := m.GetSpeed()
	if speed < 0 || speed > 4 {
		speed = 0
	}
	return ttsChoice{Provider: strings.TrimSpace(m.GetProviderId()), Model: strings.TrimSpace(m.GetModel()), Voice: strings.TrimSpace(m.GetVoice()), Speed: speed}
}

func invalid(format string, a ...any) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, a...))
}

func (s *Server) UpdateSpeechSettings(ctx context.Context, req *connect.Request[agentv1.UpdateSpeechSettingsRequest]) (*connect.Response[agentv1.UpdateSpeechSettingsResponse], error) {
	m := req.Msg
	for _, id := range []string{m.GetSyncedStt().GetProviderId(), m.GetSyncedTts().GetProviderId()} {
		if id = strings.TrimSpace(id); id != "" && !isGlobalProvider(id) {
			return nil, invalid("%q runs on this device: choose it as the device setting, not the synced one", id)
		}
	}
	for _, id := range []string{m.GetDeviceStt().GetProviderId(), m.GetDeviceTts().GetProviderId()} {
		if id = strings.TrimSpace(id); id != "" && !isDeviceProvider(id) {
			return nil, invalid("%q is a server provider: choose it as the synced setting, not the device one", id)
		}
	}
	if m.GetDeviceStt().GetProviderId() == speech.ProviderPiper || m.GetDeviceStt().GetProviderId() == speech.ProviderSay {
		return nil, invalid("%s does not do speech to text", m.GetDeviceStt().GetProviderId())
	}
	if p := m.GetDeviceTts().GetProviderId(); p == speech.ProviderParakeet || p == speech.ProviderWhisper {
		return nil, invalid("%s does not do text to speech", p)
	}

	if m.SyncedStt != nil || m.SyncedTts != nil {
		if s.Preferences == nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("synced settings are stored on the harness server; this client is not connected to one"))
		}
		if err := s.updateSyncedSpeech(ctx, func(c *speechChoice) {
			if m.SyncedStt != nil {
				c.STT = sttFrom(m.SyncedStt)
			}
			if m.SyncedTts != nil {
				c.TTS = ttsFrom(m.SyncedTts)
			}
		}); err != nil {
			return nil, err
		}
	}
	if m.DeviceStt != nil || m.DeviceTts != nil {
		s.speech.mu.Lock()
		c := s.readDeviceSpeech()
		if m.DeviceStt != nil {
			c.STT = sttFrom(m.DeviceStt)
		}
		if m.DeviceTts != nil {
			c.TTS = ttsFrom(m.DeviceTts)
		}
		err := s.writeDeviceSpeech(c)
		s.speech.mu.Unlock()
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("saving the device speech settings: %w", err))
		}
	}
	st, err := s.speechSettings(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.UpdateSpeechSettingsResponse{Settings: st}), nil
}

// updateSyncedSpeech changes the synced choice in the user's settings document (read, merge, write with a version
// check, again on a conflict), keeping every other key.
func (s *Server) updateSyncedSpeech(ctx context.Context, change func(*speechChoice)) error {
	ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	defer cancel()
	doc, err := s.userSettings(ctx)
	if err != nil {
		return syncError(err)
	}
	for i := 0; i < preferenceAttempts; i++ {
		c := syncedSpeechOf(doc)
		change(&c)
		content, err := mergeSettingsJSON(doc.GetContent(), map[string]any{speechSettingsKey: c})
		if err != nil {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the user settings document is not a JSON object: %w", err))
		}
		res, err := s.Preferences.PutDocument(ctx, &syncv1.PutDocumentRequest{Ref: userSettingsRef, Content: content, BaseVersion: proto.Int64(doc.GetVersion()), Source: "client:speech"})
		if err != nil {
			return syncError(err)
		}
		if res.GetDocument() != nil {
			return nil
		}
		doc = res.GetConflict().GetDocument()
	}
	return connect.NewError(connect.CodeAborted, errors.New("the user settings document kept changing; try again"))
}

// ---- Providers and voices ----

func kindOf(k speechv1.SpeechKind) speech.Kind {
	switch k {
	case speechv1.SpeechKind_SPEECH_KIND_STT:
		return speech.KindSTT
	case speechv1.SpeechKind_SPEECH_KIND_TTS:
		return speech.KindTTS
	}
	return 0
}

const noServer = "not connected to a harness server"

func (s *Server) ListSpeechProviders(ctx context.Context, req *connect.Request[agentv1.ListSpeechProvidersRequest]) (*connect.Response[agentv1.ListSpeechProvidersResponse], error) {
	out := &agentv1.ListSpeechProvidersResponse{Providers: s.speechLocal().LocalProviders(ctx, kindOf(req.Msg.GetKind()))}
	if s.SpeechCore == nil {
		out.ServerError = noServer
		return connect.NewResponse(out), nil
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := s.SpeechCore.ListProviders(cctx, req.Msg.GetKind())
	if err != nil {
		out.ServerError = "the harness server's speech providers are unavailable: " + err.Error()
		return connect.NewResponse(out), nil
	}
	out.Providers = append(out.Providers, res.GetProviders()...)
	out.CanManageGlobal = res.GetCanManage()
	s.rememberAccepts(res.GetProviders())
	return connect.NewResponse(out), nil
}

func (s *Server) rememberAccepts(ps []*speechv1.SpeechProvider) {
	s.speech.acceptsMu.Lock()
	defer s.speech.acceptsMu.Unlock()
	if s.speech.accepts == nil {
		s.speech.accepts = map[string][]string{}
	}
	for _, p := range ps {
		if p.GetKind() == speechv1.SpeechKind_SPEECH_KIND_STT {
			s.speech.accepts[p.GetId()] = p.GetInputMimeTypes()
		}
	}
	s.speech.acceptsAt = time.Now()
}

// acceptedTypes returns the audio types a global STT provider takes, nil when unknown.
func (s *Server) acceptedTypes(ctx context.Context, id string) []string {
	s.speech.acceptsMu.Lock()
	fresh := time.Since(s.speech.acceptsAt) < 10*time.Minute
	types, ok := s.speech.accepts[id]
	s.speech.acceptsMu.Unlock()
	if fresh && ok {
		return types
	}
	if res, err := s.SpeechCore.ListProviders(ctx, speechv1.SpeechKind_SPEECH_KIND_STT); err == nil {
		s.rememberAccepts(res.GetProviders())
		s.speech.acceptsMu.Lock()
		defer s.speech.acceptsMu.Unlock()
		return s.speech.accepts[id]
	}
	return nil
}

func (s *Server) ListSpeechVoices(ctx context.Context, req *connect.Request[agentv1.ListSpeechVoicesRequest]) (*connect.Response[agentv1.ListSpeechVoicesResponse], error) {
	id := req.Msg.GetProviderId()
	switch {
	case id == speech.ProviderSay:
		v, err := s.speechLocal().SayVoices(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return connect.NewResponse(&agentv1.ListSpeechVoicesResponse{Voices: v}), nil
	case id == speech.ProviderPiper:
		for _, p := range s.speechLocal().LocalProviders(ctx, speech.KindTTS) {
			if p.GetId() == id {
				return connect.NewResponse(&agentv1.ListSpeechVoicesResponse{Voices: p.GetVoices()}), nil
			}
		}
		return connect.NewResponse(&agentv1.ListSpeechVoicesResponse{}), nil
	case speech.IsLocal(id) || id == speech.ProviderBrowser:
		return nil, invalid("%s has no voices here", id)
	case s.SpeechCore == nil:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(noServer))
	}
	v, err := s.SpeechCore.ListVoices(ctx, id, req.Msg.GetModel())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.ListSpeechVoicesResponse{Voices: v}), nil
}

// ---- Transcribe / synthesize ----

// localError maps a local engine's error to a Connect error.
func localError(err error) error {
	switch {
	case errors.Is(err, speech.ErrUnknownModel):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, speech.ErrLocalUnavailable), errors.Is(err, speech.ErrNeedsFFmpeg):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (s *Server) TranscribeAudio(ctx context.Context, req *connect.Request[agentv1.TranscribeAudioRequest]) (*connect.Response[agentv1.TranscribeAudioResponse], error) {
	m := req.Msg
	if len(m.GetAudio()) == 0 {
		return nil, invalid("audio is empty")
	}
	if len(m.GetAudio()) > maxSpeechAudio {
		return nil, invalid("audio is larger than %d MiB", maxSpeechAudio>>20)
	}
	provider, model, language := m.GetProviderId(), m.GetModel(), m.GetLanguage()
	if provider == "" {
		st, err := s.speechSettings(ctx)
		if err != nil {
			// The server is unreachable: the device choice still works.
			st = buildSpeechSettings(speechChoice{}, s.readDeviceSpeech(), false)
		}
		provider = st.GetStt().GetProviderId()
		if model == "" {
			model = st.GetStt().GetModel()
		}
		if language == "" {
			language = st.GetStt().GetLanguage()
		}
	}
	switch {
	case provider == "":
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no speech-to-text provider is chosen"))
	case provider == speech.ProviderBrowser:
		return nil, invalid("the browser's speech recognition runs in the frontend")
	case speech.IsLocal(provider):
		local := s.speechLocal()
		pcm, err := local.Tools.ToPCM16k(ctx, m.GetAudio(), m.GetMimeType())
		if err != nil {
			return nil, localError(err)
		}
		r, err := local.Transcribe(ctx, provider, model, pcm, language)
		if err != nil {
			return nil, localError(err)
		}
		return connect.NewResponse(&agentv1.TranscribeAudioResponse{Text: r.Text, Language: language, DurationSeconds: r.Duration, ProviderId: provider, Model: r.Model}), nil
	case s.SpeechCore == nil:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(noServer))
	}
	audio, mime := m.GetAudio(), m.GetMimeType()
	if types := s.acceptedTypes(ctx, provider); types != nil && !contains(types, speech.BaseMime(mime)) {
		// The provider does not take this format (OpenRouter takes WAV / MP3): convert on this machine.
		wav, _, err := s.speechLocal().Tools.ToWAV16k(ctx, audio, mime)
		if err != nil {
			return nil, localError(err)
		}
		audio, mime = wav, "audio/wav"
	}
	res, err := s.SpeechCore.Transcribe(ctx, &speechv1.TranscribeRequest{ProviderId: provider, Model: model, Audio: audio, MimeType: mime, Language: language})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.TranscribeAudioResponse{
		Text: res.GetText(), Language: res.GetLanguage(), DurationSeconds: res.GetDurationSeconds(), ProviderId: res.GetProviderId(), Model: res.GetModel(),
	}), nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Server) SynthesizeSpeech(ctx context.Context, req *connect.Request[agentv1.SynthesizeSpeechRequest], stream *connect.ServerStream[agentv1.SynthesizeSpeechResponse]) error {
	m := req.Msg
	text := m.GetText()
	if !m.GetPlainText() {
		text = speech.SpeakableText(text)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return invalid("nothing to read aloud")
	}
	provider, model, voice, speed := m.GetProviderId(), m.GetModel(), m.GetVoice(), m.GetSpeed()
	if provider == "" || speed == 0 {
		st, err := s.speechSettings(ctx)
		if err != nil {
			st = buildSpeechSettings(speechChoice{}, s.readDeviceSpeech(), false)
		}
		t := st.GetTts()
		if provider == "" {
			provider = t.GetProviderId()
			if model == "" {
				model = t.GetModel()
			}
			if voice == "" {
				voice = t.GetVoice()
			}
		}
		if speed == 0 && t.GetProviderId() == provider {
			speed = t.GetSpeed()
		}
	}
	if speed <= 0 {
		speed = 1
	}
	switch {
	case provider == "":
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("no text-to-speech provider is chosen"))
	case provider == speech.ProviderBrowser:
		return invalid("the browser's speech synthesis runs in the frontend")
	case speech.IsLocal(provider):
		out, err := s.speechLocal().Synthesize(ctx, provider, voice, text, speed)
		if err != nil {
			return localError(err)
		}
		first := &agentv1.SynthesizeSpeechResponse{MimeType: "audio/wav", ProviderId: provider, Model: out.Model, Voice: out.Voice}
		for off := 0; off < len(out.WAV); off += speechChunk {
			msg := &agentv1.SynthesizeSpeechResponse{}
			if off == 0 {
				msg = first
			}
			msg.Audio = out.WAV[off:min(off+speechChunk, len(out.WAV))]
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
		return stream.Send(&agentv1.SynthesizeSpeechResponse{Done: true, Characters: int64(len([]rune(text)))})
	case s.SpeechCore == nil:
		return connect.NewError(connect.CodeFailedPrecondition, errors.New(noServer))
	}
	first := true
	return s.SpeechCore.Synthesize(ctx, &speechv1.SynthesizeRequest{ProviderId: provider, Model: model, Voice: voice, Text: text, Speed: speed},
		func(r *speechv1.SynthesizeResponse) error {
			msg := &agentv1.SynthesizeSpeechResponse{Audio: r.GetAudio(), MimeType: r.GetMimeType(), Done: r.GetDone(), Characters: r.GetCharacters()}
			if first {
				msg.ProviderId, msg.Model, msg.Voice = provider, model, voice
				first = false
			}
			return stream.Send(msg)
		})
}

// ---- Local models ----

func localModelProto(st speech.ModelState, supported bool) *agentv1.LocalSpeechModel {
	kind := speechv1.SpeechKind_SPEECH_KIND_UNSPECIFIED
	switch st.Kind {
	case speech.KindSTT:
		kind = speechv1.SpeechKind_SPEECH_KIND_STT
	case speech.KindTTS:
		kind = speechv1.SpeechKind_SPEECH_KIND_TTS
	}
	return &agentv1.LocalSpeechModel{
		Id: st.Key(), ProviderId: st.Provider, Model: st.ID, DisplayName: st.DisplayName, Kind: kind, Installed: st.Installed,
		SizeBytes: st.SizeBytes, Downloading: st.Downloading, Description: st.Description, Languages: st.Languages, Recommended: st.Recommended,
		Unsupported: !supported,
	}
}

func (s *Server) runtimeSupported() bool {
	rt, ok := s.speechLocal().Models.Find(speech.RuntimeKey)
	return ok && len(rt.Files) > 0
}

func (s *Server) modelState(key string) *agentv1.LocalSpeechModel {
	for _, st := range s.speechLocal().Models.States(0) {
		if st.Key() == key {
			return localModelProto(st, s.runtimeSupported())
		}
	}
	return nil
}

func (s *Server) ListLocalSpeechModels(_ context.Context, req *connect.Request[agentv1.ListLocalSpeechModelsRequest]) (*connect.Response[agentv1.ListLocalSpeechModelsResponse], error) {
	models := s.speechLocal().Models
	supported := s.runtimeSupported()
	out := &agentv1.ListLocalSpeechModelsResponse{ModelsDir: models.Dir}
	for _, st := range models.States(kindOf(req.Msg.GetKind())) {
		out.Models = append(out.Models, localModelProto(st, supported))
	}
	return connect.NewResponse(out), nil
}

func (s *Server) DownloadSpeechModel(ctx context.Context, req *connect.Request[agentv1.DownloadSpeechModelRequest], stream *connect.ServerStream[agentv1.DownloadSpeechModelResponse]) error {
	// The download belongs to the client, not to this request: it continues when the stream goes away.
	d, err := s.speechLocal().Models.Start(s.ctx, req.Msg.GetId())
	if err != nil {
		if errors.Is(err, speech.ErrUnknownModel) {
			return connect.NewError(connect.CodeNotFound, err)
		}
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	err = d.Watch(ctx, func(n, total int64) error {
		return stream.Send(&agentv1.DownloadSpeechModelResponse{DownloadedBytes: n, TotalBytes: total})
	})
	if err != nil {
		if ctx.Err() != nil {
			return connect.NewError(connect.CodeCanceled, ctx.Err())
		}
		if errors.Is(err, context.Canceled) {
			return connect.NewError(connect.CodeAborted, errors.New("the download was cancelled"))
		}
		return connect.NewError(connect.CodeUnavailable, err)
	}
	n, total := d.Progress()
	return stream.Send(&agentv1.DownloadSpeechModelResponse{DownloadedBytes: n, TotalBytes: total, Done: true, Model: s.modelState(req.Msg.GetId())})
}

func (s *Server) DeleteSpeechModel(_ context.Context, req *connect.Request[agentv1.DeleteSpeechModelRequest]) (*connect.Response[agentv1.DeleteSpeechModelResponse], error) {
	cancelled, err := s.speechLocal().Models.Delete(req.Msg.GetId())
	if err != nil {
		if errors.Is(err, speech.ErrUnknownModel) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.DeleteSpeechModelResponse{CancelledDownload: cancelled}), nil
}
