// Package lock serialises work on one machine, so an interactive `sacabox` and
// the systemd units never reconcile the same instance at once.
package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

// Machine names as default.nix allows them, plus throwaway tmp-<hex>.
var validName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ValidName reports whether name is a machine name sacabox could have created.
func ValidName(name string) bool { return validName.MatchString(name) }

// Dir is created by systemd-tmpfiles on the host (root:incus-admin, setgid).
func Dir() string {
	if d := os.Getenv("SACABOX_LOCK_DIR"); d != "" {
		return d
	}
	return "/run/sacabox"
}

// Acquire blocks until it holds the lock for name and returns its release function.
func Acquire(name string) (func(), error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid machine name %q", name)
	}
	dir := Dir()
	if err := os.MkdirAll(dir, 0o770|os.ModeSetgid); err != nil {
		return nil, fmt.Errorf("lock dir: %w", err)
	}
	// flock needs no write access, so other incus-admin users can lock it whatever the umask.
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDONLY|syscall.O_NOFOLLOW, 0o660)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", name, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintf(os.Stderr, "sacabox: waiting for another sacabox working on %s\n", name)
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", name, err)
		}
	}
	return func() { f.Close() }, nil
}
