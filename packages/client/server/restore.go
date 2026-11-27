package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/session"
)

// RestoreFromServer writes the server's transcript of a session into the local store at cwd, unchanged and under the same
// id, when this machine has none of it: a cloud session's new container resumes it (docs/design/cloud-sessions.md,
// decision 8). The uploader sends it back idempotently, by entry uuid. Subagent transcripts are not copied.
func (s *Server) RestoreFromServer(ctx context.Context, id, cwd string) error {
	if !validSessionID(id) {
		return fmt.Errorf("%q is not a session id", id)
	}
	store := session.Store{Root: config.ProjectsDir()}
	if _, err := store.Find("", id); err == nil {
		return nil
	}
	raw, err := s.remoteEntries(ctx, id)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return fmt.Errorf("session %s has no transcript on the harness server", id)
	}
	path := store.Path(cwd, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".restore"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range raw {
		if line := e.GetEntryJson(); line != "" {
			_, _ = w.WriteString(line)
			_ = w.WriteByte('\n')
		}
	}
	if err := errors.Join(w.Flush(), f.Close()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
