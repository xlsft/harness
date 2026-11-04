package computeruse

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		app  App
		want Tier
	}{
		{App{ID: "com.apple.Safari", Name: "Safari"}, TierRead},
		{App{ID: "com.google.Chrome.canary", Name: "Google Chrome Canary"}, TierRead},
		{App{ID: "org.mozilla.firefox", Name: "Firefox"}, TierRead},
		{App{ID: "app.zen-browser.zen", Name: "Zen"}, TierRead},
		{App{ID: "unknown.rebuild", Name: "Firefox Developer Edition"}, TierRead},
		{App{ID: "company.thebrowser.Browser", Name: "Arc"}, TierRead},
		{App{ID: "com.apple.Terminal", Name: "Terminal"}, TierClick},
		{App{ID: "com.googlecode.iterm2", Name: "iTerm2"}, TierClick},
		{App{ID: "com.microsoft.VSCode", Name: "Visual Studio Code"}, TierClick},
		{App{ID: "com.jetbrains.goland", Name: "GoLand"}, TierClick},
		{App{ID: "com.todesktop.230313mzl4w4u92", Name: "Cursor"}, TierClick},
		{App{ID: "com.apple.dt.Xcode", Name: "Xcode"}, TierClick},
		{App{ID: "com.apple.ScriptEditor2", Name: "Script Editor"}, TierClick},
		{App{ID: "com.apple.shortcuts", Name: "Shortcuts"}, TierClick},
		{App{ID: "com.1password.1password", Name: "1Password"}, TierBlocked},
		{App{ID: "com.bitwarden.desktop", Name: "Bitwarden"}, TierBlocked},
		{App{ID: "unknown.vault", Name: "My KeePass Fork"}, TierBlocked},
		{App{ID: "com.apple.Passwords", Name: "Passwords"}, TierBlocked},
		{App{ID: "com.apple.keychainaccess", Name: "Keychain Access"}, TierBlocked},
		{App{ID: "com.apple.systempreferences", Name: "System Settings"}, TierBlocked},
		{App{ID: "com.apple.SecurityAgent", Name: "SecurityAgent"}, TierBlocked},
		{App{ID: "dev.xlsft.harness", Name: "xlsft`s harness"}, TierBlocked},
		{App{ID: "com.apple.Notes", Name: "Notes"}, TierFull},
		{App{ID: "com.apple.finder", Name: "Finder"}, TierFull},
		// Words, not substrings.
		{App{ID: "com.apple.archiveutility", Name: "Archive Utility"}, TierFull},
		{App{ID: "com.example.operator", Name: "Operator Console Tools Hub"}, TierClick},
		{App{ID: "com.example.diagram", Name: "Diagram Maker"}, TierFull},
	}
	for _, c := range cases {
		if got := Classify(c.app); got != c.want {
			t.Errorf("%s (%s): %s, want %s", c.app.Name, c.app.ID, got, c.want)
		}
	}
}

func TestEncodeFrameScalesAndKeepsSize(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3000, 1000))
	for x := range 3000 {
		src.Set(x, 500, color.White)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	data, w, h, err := encodeFrame(buf.Bytes(), 1568)
	if err != nil {
		t.Fatal(err)
	}
	if w != 1568 || h != 522 {
		t.Fatalf("%d×%d", w, h)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("%+v", b)
	}
	data, w, h, err = encodeFrame(buf.Bytes(), 4000)
	if err != nil || w != 3000 || h != 1000 || len(data) == 0 {
		t.Fatalf("%d×%d %v", w, h, err)
	}
	if _, _, _, err := encodeFrame([]byte("not an image"), 100); err == nil {
		t.Fatal("no error for garbage")
	}
}
