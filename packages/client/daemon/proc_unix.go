//go:build !windows

package daemon

import (
	"os"
	"path/filepath"
	"syscall"
)

func workerAttr(uid, gid int) *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), NoSetGroups: false, Groups: []uint32{}}}
	setDeathSignal(attr)
	return attr
}

func terminate(p *os.Process) { _ = p.Signal(syscall.SIGTERM) }

// prepareHome makes a worker's home and hands it over. 0700: nothing in it is for other users.
func prepareHome(home string, uid, gid int) error {
	if err := os.MkdirAll(filepath.Dir(home), 0o711); err != nil {
		return err
	}
	for _, d := range []string{home, filepath.Join(home, ".harness"), filepath.Join(home, "tmp"), filepath.Join(home, "workspaces")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Chown(d, uid, gid); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func diskFree(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
