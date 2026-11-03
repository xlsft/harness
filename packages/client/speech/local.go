package speech

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	speechv1 "github.com/xlsft/harness/packages/client/gen/harness/speech/v1"
)

// Local runs the engines of this machine: Parakeet and Whisper (speech to text) and Piper (text to speech) through
// the sherpa-onnx command line tools that Models downloads into HARNESS_HOME, and macOS `say`. Nothing leaves the
// machine; the only network use is fetching model files (Models).
type Local struct {
	Models *Models
	Tools  Tools
	// Exec runs a tool and returns its combined output; nil runs it as a child process.
	Exec func(ctx context.Context, bin string, args, env []string) ([]byte, error)
	// GOOS overrides runtime.GOOS (tests).
	GOOS string

	sayOnce   sync.Once
	sayVoices []*speechv1.SpeechVoice
	sayErr    error
}

// ErrLocalUnavailable wraps every "cannot run here" error of a local engine.
var ErrLocalUnavailable = errors.New("local speech engine unavailable")

func (l *Local) goos() string {
	if l.GOOS != "" {
		return l.GOOS
	}
	return runtime.GOOS
}

func (l *Local) exec(ctx context.Context, bin string, args, env []string) ([]byte, error) {
	if l.Exec != nil {
		return l.Exec(ctx, bin, args, env)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = 2 * time.Second
	return cmd.CombinedOutput()
}

func threads() string { return strconv.Itoa(max(1, min(4, runtime.NumCPU()))) }

// runtimeTool returns the path of a sherpa-onnx tool and the environment that finds its shared libraries.
func (l *Local) runtimeTool(rel string) (string, []string, error) {
	rt, ok := l.Models.Find(RuntimeKey)
	if !ok || len(rt.Files) == 0 {
		return "", nil, fmt.Errorf("%w: the sherpa-onnx runtime is not available for %s/%s", ErrLocalUnavailable, runtime.GOOS, runtime.GOARCH)
	}
	if ok, _ := l.Models.Installed(rt); !ok {
		return "", nil, fmt.Errorf("%w: the speech runtime is not installed; download a model first", ErrLocalUnavailable)
	}
	dir := l.Models.Path(rt)
	lib := filepath.Join(dir, "lib")
	bin := filepath.Join(dir, "bin")
	env := []string{"LD_LIBRARY_PATH=" + lib, "DYLD_LIBRARY_PATH=" + lib}
	if l.goos() == "windows" {
		env = []string{"PATH=" + bin + string(os.PathListSeparator) + lib + string(os.PathListSeparator) + os.Getenv("PATH")}
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), env, nil
}

// model returns a ready catalog model of a provider: the given id, else the recommended (or first) installed one.
func (l *Local) model(provider, id string) (LocalModel, error) {
	if id != "" {
		x, ok := l.Models.Find(provider + "/" + id)
		if !ok {
			return LocalModel{}, fmt.Errorf("%w %q for %s", ErrUnknownModel, id, provider)
		}
		if !l.Models.Ready(x) {
			return LocalModel{}, fmt.Errorf("%w: %s is not downloaded", ErrLocalUnavailable, x.DisplayName)
		}
		return x, nil
	}
	var first *LocalModel
	for _, x := range l.Models.Of(provider) {
		if !l.Models.Ready(x) {
			continue
		}
		if x.Recommended {
			return x, nil
		}
		if first == nil {
			first = &x
		}
	}
	if first == nil {
		return LocalModel{}, fmt.Errorf("%w: no %s model is downloaded", ErrLocalUnavailable, provider)
	}
	return *first, nil
}

// ---------------------------------------------------------------------------------------------------------------
// Speech to text
// ---------------------------------------------------------------------------------------------------------------

// Transcription is a local recognizer's result.
type Transcription struct {
	Text     string
	Model    string
	Duration float64
}

// Transcribe recognizes 16 kHz mono PCM with a local engine (ProviderParakeet or ProviderWhisper); model "" picks
// an installed one; language "" detects it (Whisper; Parakeet always detects).
func (l *Local) Transcribe(ctx context.Context, provider, model string, pcm PCM, language string) (Transcription, error) {
	x, err := l.model(provider, model)
	if err != nil {
		return Transcription{}, err
	}
	bin, env, err := l.runtimeTool(SherpaOffline)
	if err != nil {
		return Transcription{}, err
	}
	tmp, err := os.MkdirTemp("", "harness-stt-")
	if err != nil {
		return Transcription{}, err
	}
	defer os.RemoveAll(tmp)
	wav := filepath.Join(tmp, "audio.wav")
	if err := os.WriteFile(wav, EncodeWAV(pcm.Resample(16000)), 0o600); err != nil {
		return Transcription{}, err
	}
	dir := l.Models.Path(x)
	f := func(name string) string { return filepath.Join(dir, name) }
	args := []string{"--num-threads=" + threads()}
	switch provider {
	case ProviderParakeet:
		args = append(args, "--tokens="+f("tokens.txt"), "--encoder="+f("encoder.int8.onnx"), "--decoder="+f("decoder.int8.onnx"),
			"--joiner="+f("joiner.int8.onnx"), "--model-type=nemo_transducer", "--decoding-method=greedy_search")
	case ProviderWhisper:
		args = append(args, "--tokens="+f(x.ID+"-tokens.txt"), "--whisper-encoder="+f(x.ID+"-encoder.int8.onnx"),
			"--whisper-decoder="+f(x.ID+"-decoder.int8.onnx"), "--whisper-task=transcribe")
		if lang := whisperLanguage(language); lang != "" {
			args = append(args, "--whisper-language="+lang)
		}
	default:
		return Transcription{}, fmt.Errorf("%s is not a local speech-to-text engine", provider)
	}
	args = append(args, wav)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := l.exec(ctx, bin, args, env)
	if err != nil {
		return Transcription{}, fmt.Errorf("%s failed: %v: %s", x.DisplayName, err, lastLine(string(out)))
	}
	text, ok := parseSherpaText(out)
	if !ok {
		return Transcription{}, fmt.Errorf("%s printed no result: %s", x.DisplayName, lastLine(string(out)))
	}
	return Transcription{Text: text, Model: x.ID, Duration: pcm.Duration()}, nil
}

var reLangPrefix = regexp.MustCompile(`^[A-Za-z]{2,3}`)

// whisperLanguage is the ISO 639-1 code Whisper takes ("en-US" -> "en").
func whisperLanguage(lang string) string {
	return strings.ToLower(reLangPrefix.FindString(strings.TrimSpace(lang)))
}

// parseSherpaText finds the JSON result line sherpa-onnx-offline prints ({"text": ..., ...}).
func parseSherpaText(out []byte) (string, bool) {
	var text string
	found := false
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "{") || !strings.Contains(line, `"text"`) {
			continue
		}
		var r struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(line), &r) == nil {
			text, found = strings.TrimSpace(r.Text), true
		}
	}
	return text, found
}

// ---------------------------------------------------------------------------------------------------------------
// Text to speech
// ---------------------------------------------------------------------------------------------------------------

// Speech is synthesized audio (a WAV file).
type Speech struct {
	WAV   []byte
	Model string
	Voice string
}

// maxPiperChars bounds the text of one sherpa-onnx-offline-tts run (it takes the text as an argument).
const maxPiperChars = 1500

// Synthesize speaks plain text with a local engine (ProviderPiper: voice is a Piper voice id, "" an installed one;
// ProviderSay: a `say` voice name, "" the system voice). speed 1 is normal.
func (l *Local) Synthesize(ctx context.Context, provider, voice, text string, speed float64) (Speech, error) {
	if speed <= 0 {
		speed = 1
	}
	speed = max(0.5, min(2, speed))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	switch provider {
	case ProviderPiper:
		return l.piper(ctx, voice, text, speed)
	case ProviderSay:
		return l.say(ctx, voice, text, speed)
	}
	return Speech{}, fmt.Errorf("%s is not a local text-to-speech engine", provider)
}

func (l *Local) piper(ctx context.Context, voice, text string, speed float64) (Speech, error) {
	x, err := l.model(ProviderPiper, voice)
	if err != nil {
		return Speech{}, err
	}
	bin, env, err := l.runtimeTool(SherpaOfflineTTS)
	if err != nil {
		return Speech{}, err
	}
	tmp, err := os.MkdirTemp("", "harness-tts-")
	if err != nil {
		return Speech{}, err
	}
	defer os.RemoveAll(tmp)
	dir := l.Models.Path(x)
	var all PCM
	for i, piece := range SplitText(text, maxPiperChars) {
		// The text is the last argument; a leading dash would read as an option.
		piece = strings.TrimLeft(strings.ReplaceAll(piece, "\n", " "), "-– ")
		if piece == "" {
			continue
		}
		out := filepath.Join(tmp, fmt.Sprintf("piece-%d.wav", i))
		args := []string{
			"--vits-model=" + filepath.Join(dir, x.ID+".onnx"),
			"--vits-tokens=" + filepath.Join(dir, "tokens.txt"),
			"--vits-data-dir=" + filepath.Join(dir, "espeak-ng-data"),
			"--vits-length-scale=" + strconv.FormatFloat(1/speed, 'f', 3, 64),
			"--num-threads=" + threads(),
			"--output-filename=" + out,
			piece,
		}
		if b, err := l.exec(ctx, bin, args, env); err != nil {
			return Speech{}, fmt.Errorf("piper failed: %v: %s", err, lastLine(string(b)))
		}
		b, err := os.ReadFile(out)
		if err != nil {
			return Speech{}, fmt.Errorf("piper wrote no audio: %w", err)
		}
		p, err := DecodeWAV(b)
		if err != nil {
			return Speech{}, fmt.Errorf("piper wrote unreadable audio: %w", err)
		}
		if all.Rate != 0 && p.Rate != all.Rate {
			p = p.Resample(all.Rate)
		}
		all.Rate = p.Rate
		all.Samples = append(all.Samples, p.Samples...)
	}
	if all.Rate == 0 {
		return Speech{}, errors.New("nothing to say")
	}
	return Speech{WAV: EncodeWAV(all), Model: "", Voice: x.ID}, nil
}

func (l *Local) sayBinary() (string, error) {
	if l.goos() != "darwin" {
		return "", fmt.Errorf("%w: say exists on macOS only", ErrLocalUnavailable)
	}
	p, err := l.Tools.Find("", "say")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrLocalUnavailable, err)
	}
	return p, nil
}

func (l *Local) say(ctx context.Context, voice, text string, speed float64) (Speech, error) {
	bin, err := l.sayBinary()
	if err != nil {
		return Speech{}, err
	}
	tmp, err := os.MkdirTemp("", "harness-say-")
	if err != nil {
		return Speech{}, err
	}
	defer os.RemoveAll(tmp)
	in, out := filepath.Join(tmp, "text.txt"), filepath.Join(tmp, "speech.wav")
	if err := os.WriteFile(in, []byte(text), 0o600); err != nil {
		return Speech{}, err
	}
	args := []string{"-r", strconv.Itoa(int(175 * speed)), "-o", out, "--file-format=WAVE", "--data-format=LEI16@22050", "-f", in}
	if voice != "" {
		args = append([]string{"-v", voice}, args...)
	}
	if b, err := l.exec(ctx, bin, args, nil); err != nil {
		return Speech{}, fmt.Errorf("say failed: %v: %s", err, lastLine(string(b)))
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return Speech{}, fmt.Errorf("say wrote no audio: %w", err)
	}
	return Speech{WAV: b, Voice: voice}, nil
}

// reSayVoice matches a `say -v ?` line: "Name (Variant)   en_US    # Sample sentence."
var reSayVoice = regexp.MustCompile(`^(.+?)\s+([a-z]{2,3}_[A-Za-z0-9]+)\s+#\s*(.*)$`)

// SayVoices lists the macOS voices (`say -v ?`, cached for the process).
func (l *Local) SayVoices(ctx context.Context) ([]*speechv1.SpeechVoice, error) {
	l.sayOnce.Do(func() {
		bin, err := l.sayBinary()
		if err != nil {
			l.sayErr = err
			return
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out, err := l.exec(ctx, bin, []string{"-v", "?"}, nil)
		if err != nil {
			l.sayErr = fmt.Errorf("say -v ? failed: %v", err)
			return
		}
		l.sayVoices = ParseSayVoices(string(out))
	})
	return l.sayVoices, l.sayErr
}

// ParseSayVoices parses the output of `say -v ?`.
func ParseSayVoices(out string) []*speechv1.SpeechVoice {
	var voices []*speechv1.SpeechVoice
	for _, line := range strings.Split(out, "\n") {
		m := reSayVoice.FindStringSubmatch(strings.TrimRight(line, " \r"))
		if m == nil {
			continue
		}
		voices = append(voices, &speechv1.SpeechVoice{
			Id: strings.TrimSpace(m[1]), DisplayName: strings.TrimSpace(m[1]), Language: strings.ReplaceAll(m[2], "_", "-"), Description: m[3], Installed: true,
		})
	}
	return voices
}

// SplitText splits text into pieces of at most max characters at paragraph, sentence, then word boundaries.
func SplitText(text string, max int) []string {
	var out []string
	rest := []rune(strings.TrimSpace(text))
	for len(rest) > max {
		window := string(rest[:max])
		cut := -1
		for _, sep := range []string{"\n\n", ". ", "! ", "? ", "。", "\n", "; ", ", ", " "} {
			if i := strings.LastIndex(window, sep); i > 0 && (i > len(window)/3 || sep == " ") {
				cut = len([]rune(window[:i+len(sep)]))
				break
			}
		}
		if cut <= 0 {
			cut = max
		}
		if piece := strings.TrimSpace(string(rest[:cut])); piece != "" {
			out = append(out, piece)
		}
		rest = []rune(strings.TrimSpace(string(rest[cut:])))
	}
	if len(rest) > 0 {
		out = append(out, string(rest))
	}
	return out
}
