package speech

import (
	"context"
	"os/exec"
	"testing"
)

// TestSayReal runs the real macOS say (skipped elsewhere): voices parse and a WAV comes back.
func TestSayReal(t *testing.T) {
	if _, err := exec.LookPath("say"); err != nil || testing.Short() {
		t.Skip("no say")
	}
	l := &Local{Models: NewModels(t.TempDir(), nil, nil)}
	voices, err := l.SayVoices(context.Background())
	if err != nil || len(voices) == 0 {
		t.Fatalf("voices %v %d", err, len(voices))
	}
	out, err := l.Synthesize(context.Background(), ProviderSay, "", "Hello.", 1.2)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := DecodeWAV(out.WAV); err != nil || p.Rate != 22050 || p.Duration() < 0.2 {
		t.Fatalf("wav %v %+v", err, p.Rate)
	}
}
