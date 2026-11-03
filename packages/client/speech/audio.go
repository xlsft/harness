package speech

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Tools finds optional system binaries (ffmpeg, macOS say). Each is looked up on PATH and in the usual package
// manager directories (a desktop app's sidecar often starts with a minimal PATH), unless its environment variable
// names a path. The speech engines themselves never need a system install (see Models).
type Tools struct {
	// LookPath finds a binary; nil: exec.LookPath plus the extra directories.
	LookPath func(name string) (string, error)
}

// EnvFFmpeg overrides where ffmpeg is. ffmpeg is optional: the local recognizers read WAV without it, and only
// compressed recordings (webm/opus from MediaRecorder) need it for local engines.
const EnvFFmpeg = "HARNESS_FFMPEG"

var extraDirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/snap/bin"}

// Find returns the path of the first binary in names that exists (env, when set, wins), or an error.
func (t Tools) Find(env string, names ...string) (string, error) {
	if env != "" {
		if p := strings.TrimSpace(os.Getenv(env)); p != "" {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
			return "", fmt.Errorf("%s=%s does not exist", env, p)
		}
	}
	look := t.LookPath
	if look == nil {
		look = lookPath
	}
	for _, n := range names {
		if p, err := look(n); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found", names[0])
}

func lookPath(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, d := range []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin")} {
			if p := filepath.Join(d, name); isExecutable(p) {
				return p, nil
			}
		}
	}
	for _, d := range extraDirs {
		if p := filepath.Join(d, name); isExecutable(p) {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && (runtime.GOOS == "windows" || st.Mode()&0o111 != 0)
}

// FFmpeg is the ffmpeg binary, or an error saying how to get it.
func (t Tools) FFmpeg() (string, error) {
	p, err := t.Find(EnvFFmpeg, "ffmpeg")
	if err != nil {
		return "", fmt.Errorf("%w: install ffmpeg (%s) to convert compressed audio", err, installHint("ffmpeg"))
	}
	return p, nil
}

func installHint(pkg string) string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install " + pkg
	case "windows":
		return "winget install " + pkg
	default:
		return "your package manager's " + pkg + " package"
	}
}

// IsWAV reports whether the MIME type names WAV audio.
func IsWAV(mime string) bool {
	switch BaseMime(mime) {
	case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		return true
	}
	return false
}

// BaseMime is the MIME type without parameters, lower case.
func BaseMime(mime string) string {
	return strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))
}

// ErrNeedsFFmpeg means compressed audio arrived and ffmpeg is not installed.
var ErrNeedsFFmpeg = errors.New("ffmpeg is needed to convert this audio")

// ToPCM16k decodes audio into 16 kHz mono PCM, what the local recognizers take: WAV in Go, anything else
// (webm/opus from MediaRecorder, ogg, mp3, m4a) with ffmpeg. Without ffmpeg only WAV works (ErrNeedsFFmpeg).
func (t Tools) ToPCM16k(ctx context.Context, audio []byte, mime string) (PCM, error) {
	if p, err := DecodeWAV(audio); err == nil {
		return p.Resample(16000), nil
	} else if IsWAV(mime) {
		return PCM{}, fmt.Errorf("cannot read the WAV audio: %w", err)
	}
	ff, err := t.FFmpeg()
	if err != nil {
		return PCM{}, fmt.Errorf("%w (%s): %v; or record WAV audio", ErrNeedsFFmpeg, BaseMime(mime), err)
	}
	wav, err := runFFmpeg(ctx, ff, audio)
	if err != nil {
		return PCM{}, err
	}
	return DecodeWAV(wav)
}

// ToWAV16k is ToPCM16k as a WAV file.
func (t Tools) ToWAV16k(ctx context.Context, audio []byte, mime string) ([]byte, PCM, error) {
	p, err := t.ToPCM16k(ctx, audio, mime)
	if err != nil {
		return nil, PCM{}, err
	}
	return EncodeWAV(p), p, nil
}

func runFFmpeg(ctx context.Context, ff string, audio []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ff, "-nostdin", "-hide_banner", "-loglevel", "error", "-i", "pipe:0", "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-f", "wav", "pipe:1")
	cmd.Stdin = bytes.NewReader(audio)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg could not convert the audio: %v: %s", err, lastLine(stderr.String()))
	}
	return out.Bytes(), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
