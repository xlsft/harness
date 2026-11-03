package speech

import (
	"context"
	"fmt"
	"runtime"

	speechv1 "github.com/xlsft/harness/packages/client/gen/harness/speech/v1"
)

// wavTypes are what the local recognizers read without ffmpeg.
var wavTypes = []string{"audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave"}

// compressedTypes are what ffmpeg converts for them.
var compressedTypes = []string{"audio/webm", "audio/ogg", "audio/mpeg", "audio/mp3", "audio/mp4", "audio/m4a", "audio/x-m4a", "audio/flac", "audio/aac"}

// IsLocal reports whether a provider id is one of this machine's engines.
func IsLocal(id string) bool {
	switch id {
	case ProviderParakeet, ProviderWhisper, ProviderPiper, ProviderSay:
		return true
	}
	return false
}

// LocalProviders describes the local engines for a kind (0: both): installed models, voices, availability.
func (l *Local) LocalProviders(ctx context.Context, kind Kind) []*speechv1.SpeechProvider {
	var out []*speechv1.SpeechProvider
	if kind == 0 || kind == KindSTT {
		mimes := wavTypes
		if _, err := l.Tools.FFmpeg(); err == nil {
			mimes = append(append([]string{}, wavTypes...), compressedTypes...)
		}
		out = append(out,
			l.sttProvider(ProviderParakeet, "NVIDIA Parakeet", "PA", "Fast, accurate on-device dictation (25 European languages). Runs on this machine.", false, mimes),
			l.sttProvider(ProviderWhisper, "Whisper", "WH", "OpenAI Whisper on this machine, 99 languages.", true, mimes))
	}
	if kind == 0 || kind == KindTTS {
		out = append(out, l.piperProvider())
		if p := l.sayProvider(ctx); p != nil {
			out = append(out, p)
		}
	}
	return out
}

func (l *Local) runtimeState() (supported, installed bool) {
	rt, ok := l.Models.Find(RuntimeKey)
	if !ok || len(rt.Files) == 0 {
		return false, false
	}
	ok, _ = l.Models.Installed(rt)
	return true, ok
}

func (l *Local) modelsOf(provider string) (models []*speechv1.SpeechModel, def string, anyReady bool) {
	for _, x := range l.Models.Of(provider) {
		ok, size := l.Models.Installed(x)
		if !ok {
			size = x.Size()
		}
		ready := ok && l.Models.Ready(x)
		models = append(models, &speechv1.SpeechModel{Id: x.ID, DisplayName: x.DisplayName, Description: x.Description, Installed: ready, SizeBytes: size, Languages: x.Languages})
		if ready && (def == "" || x.Recommended) {
			def = x.ID
		}
		anyReady = anyReady || ready
	}
	if def == "" {
		for _, x := range l.Models.Of(provider) {
			if x.Recommended {
				def = x.ID
			}
		}
	}
	return models, def, anyReady
}

func (l *Local) sttProvider(id, name, icon, desc string, language bool, mimes []string) *speechv1.SpeechProvider {
	supported, rt := l.runtimeState()
	models, def, ready := l.modelsOf(id)
	p := &speechv1.SpeechProvider{
		Id: id, DisplayName: name, Kind: speechv1.SpeechKind_SPEECH_KIND_STT, Location: speechv1.SpeechLocation_SPEECH_LOCATION_LOCAL,
		Configured: rt, Available: ready, Models: models, DefaultModel: def, SupportsLanguage: language, InputMimeTypes: mimes, Icon: icon, Description: desc,
	}
	switch {
	case !supported:
		p.UnavailableReason = fmt.Sprintf("Not available on %s/%s", runtime.GOOS, runtime.GOARCH)
	case !ready:
		p.UnavailableReason = "Download a model to use it (the speech runtime is downloaded with the first model)"
	}
	return p
}

func (l *Local) piperProvider() *speechv1.SpeechProvider {
	supported, rt := l.runtimeState()
	p := &speechv1.SpeechProvider{
		Id: ProviderPiper, DisplayName: "Piper", Kind: speechv1.SpeechKind_SPEECH_KIND_TTS, Location: speechv1.SpeechLocation_SPEECH_LOCATION_LOCAL,
		Configured: rt, SupportsSpeed: true, Icon: "PI", Description: "Natural offline voices, one download per voice. Runs on this machine.",
	}
	for _, x := range l.Models.Of(ProviderPiper) {
		ok, size := l.Models.Installed(x)
		if !ok {
			size = x.Size()
		}
		ready := ok && l.Models.Ready(x)
		lang := ""
		if len(x.Languages) > 0 {
			lang = x.Languages[0]
		}
		p.Voices = append(p.Voices, &speechv1.SpeechVoice{Id: x.ID, DisplayName: x.DisplayName, Language: lang, Description: x.Description, Installed: ready, SizeBytes: size})
		if ready && (p.DefaultVoice == "" || x.Recommended) {
			p.DefaultVoice = x.ID
		}
		p.Available = p.Available || ready
	}
	switch {
	case !supported:
		p.UnavailableReason = fmt.Sprintf("Not available on %s/%s", runtime.GOOS, runtime.GOARCH)
	case !p.Available:
		p.UnavailableReason = "Download a voice to use it (the speech runtime is downloaded with the first voice)"
	}
	return p
}

// sayProvider is nil outside macOS.
func (l *Local) sayProvider(ctx context.Context) *speechv1.SpeechProvider {
	if l.goos() != "darwin" {
		return nil
	}
	p := &speechv1.SpeechProvider{
		Id: ProviderSay, DisplayName: "macOS voices", Kind: speechv1.SpeechKind_SPEECH_KIND_TTS, Location: speechv1.SpeechLocation_SPEECH_LOCATION_LOCAL,
		SupportsSpeed: true, Icon: "OS", Description: "The system voices of macOS (more in System Settings > Accessibility > Spoken Content).",
	}
	voices, err := l.SayVoices(ctx)
	if err != nil {
		p.UnavailableReason = err.Error()
		return p
	}
	p.Configured, p.Available, p.Voices = true, true, voices
	return p
}
