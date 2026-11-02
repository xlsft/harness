package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// Fork copies the transcript at srcPath into a new session newID at
// dstPath, rewriting sessionId on every line (other fields are kept
// verbatim) and recording the parent in a harness-meta entry. With
// atMessageID set, the fork branches before the turn started by that user
// message (the conversation up to that checkpoint).
func Fork(srcPath, dstPath, srcID, newID, atMessageID string) error {
	if _, err := os.Stat(dstPath); err == nil {
		return fmt.Errorf("session %s already exists", newID)
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	w := bufio.NewWriter(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1024*1024), 256*1024*1024)
	var entries []Entry
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		if _, ok := m["sessionId"]; ok {
			m["sessionId"] = newID
		}
		data, err := json.Marshal(m)
		if err != nil {
			continue
		}
		w.Write(append(data, '\n'))
		var e Entry
		if json.Unmarshal(data, &e) == nil {
			entries = append(entries, e)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	ts := now()
	meta, _ := json.Marshal(Entry{Type: TypeMeta, SessionID: newID, ForkedFrom: srcID, Timestamp: ts})
	w.Write(append(meta, '\n'))
	if atMessageID != "" {
		parent, ok := TurnParent(entries, atMessageID)
		if !ok {
			return errors.New("checkpoint " + atMessageID + " is not in the source transcript")
		}
		marker := Entry{Type: TypeSystem, Subtype: SubtypeRewind, SessionID: newID, UUID: uuid.NewString(), ParentUUID: parent, Content: "Forked at checkpoint " + atMessageID, Timestamp: ts, UserType: "external"}
		data, _ := json.Marshal(marker)
		w.Write(append(data, '\n'))
	}
	return w.Flush()
}
