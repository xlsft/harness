package selfupdate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// ErrBadSignature means a signature did not verify, or was not made by the key this build trusts.
var ErrBadSignature = errors.New("signature does not verify")

// verifySignature checks data against a signature and public key as `tauri signer` writes them: each is the base64 of
// a minisign text file. The desktop updater checks the same key, so a release has one signing key.
func verifySignature(publicKey, signature string, data []byte) error {
	keyID, key, err := parsePublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("update public key: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return fmt.Errorf("%w: not base64", ErrBadSignature)
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) < 4 {
		return fmt.Errorf("%w: malformed", ErrBadSignature)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(sig) != 2+8+ed25519.SignatureSize {
		return fmt.Errorf("%w: malformed", ErrBadSignature)
	}
	if !bytes.Equal(sig[2:10], keyID) {
		return fmt.Errorf("%w: signed with another key", ErrBadSignature)
	}
	msg := data
	switch string(sig[:2]) {
	case "ED": // what current tauri and minisign write: the BLAKE2b-512 of the file is signed
		sum := blake2b.Sum512(data)
		msg = sum[:]
	case "Ed":
	default:
		return fmt.Errorf("%w: unknown algorithm", ErrBadSignature)
	}
	if !ed25519.Verify(key, msg, sig[10:]) {
		return ErrBadSignature
	}
	// The second signature covers the first and the trusted comment.
	comment, ok := strings.CutPrefix(lines[2], "trusted comment: ")
	if !ok {
		return fmt.Errorf("%w: malformed", ErrBadSignature)
	}
	global, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil || len(global) != ed25519.SignatureSize {
		return fmt.Errorf("%w: malformed", ErrBadSignature)
	}
	if !ed25519.Verify(key, append(append([]byte{}, sig[10:]...), comment...), global) {
		return ErrBadSignature
	}
	return nil
}

func parsePublicKey(s string) (keyID []byte, key ed25519.PublicKey, err error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, nil, errors.New("not base64")
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) < 2 {
		return nil, nil, errors.New("malformed")
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(b) != 2+8+ed25519.PublicKeySize || string(b[:2]) != "Ed" {
		return nil, nil, errors.New("malformed")
	}
	return b[2:10], ed25519.PublicKey(b[10:]), nil
}
