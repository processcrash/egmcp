package store

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// writeWithLock writes Instance inst to path under an exclusive
// advisory lock, atomically replacing the previous contents. The
// lock is implemented as a sibling lockfile so the same code runs
// on Linux, macOS, and Windows without syscall differences.
//
//   1. Create the lockfile exclusively (O_CREATE|O_EXCL). On
//      contention, back off and retry briefly.
//   2. Marshal the instance and write to <path>.tmp.
//   3. Rename atomically to <path>.
//   4. Release the lockfile.
//
// The atomic rename ensures readers either see the previous valid
// instance or the new one in full — never a half-written file.
func writeWithLock(path string, inst *Instance) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(inst)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	lockPath := path + ".lock"
	release, err := acquireLock(lockPath)
	if err != nil {
		return fmt.Errorf("lock %s: %w", lockPath, err)
	}
	defer release()

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// acquireLock creates lockPath exclusively, blocking for up to ~2s on
// contention. The returned release func closes the file handle and
// removes the lockfile; the OS-level exclusive create guarantees no
// two processes hold the same lockfile.
func acquireLock(lockPath string) (release func(), err error) {
	const maxAttempts = 40
	for i := 0; i < maxAttempts; i++ {
		f, ferr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if ferr == nil {
			return func() {
				_ = f.Close()
				_ = os.Remove(lockPath)
			}, nil
		}
		if !os.IsExist(ferr) {
			return nil, ferr
		}
		// 50ms × 40 = 2s total worst-case wait.
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("could not acquire %s within timeout", lockPath)
}
