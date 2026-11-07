// Package guest provisions the inside of an instance over the Incus agent:
// NixOS configuration, init script, dotfiles and Home Manager.
package guest

import (
	"archive/tar"
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"sacabox/internal/bin"
	"sacabox/internal/incus"
	"sacabox/internal/manifest"
)

// The guest user is always uid/gid 1000 (see machine.nix and guest-nixos.nix).
const uid = 1000

const nixosBin = "/run/current-system/sw/bin/"

type Guest struct {
	S    incus.Server
	Name string
	User string
	Home string
}

func (g *Guest) run(o incus.ExecOpts) error {
	if o.Stdout == nil {
		o.Stdout = &sanitized{w: os.Stdout}
	}
	if o.Stderr == nil {
		o.Stderr = &sanitized{w: os.Stderr}
	}
	code, err := g.S.Exec(g.Name, o)
	if err != nil {
		return fmt.Errorf("%s: %s: %w", g.Name, o.Cmd[0], err)
	}
	if code != 0 {
		return fmt.Errorf("%s: %s exited with status %d", g.Name, strings.Join(o.Cmd, " "), code)
	}
	return nil
}

func (g *Guest) asUser(o incus.ExecOpts) incus.ExecOpts {
	o.User, o.Group, o.Cwd = uid, uid, g.Home
	env := map[string]string{"HOME": g.Home, "USER": g.User}
	for k, v := range o.Env {
		env[k] = v
	}
	o.Env = env
	return o
}

// WaitAgent waits until commands can run in the guest.
func (g *Guest) WaitAgent() error {
	for range 240 {
		if code, err := g.S.Exec(g.Name, incus.ExecOpts{Cmd: []string{"true"}}); err == nil && code == 0 {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s: incus-agent never answered (image must be an agent-capable VM/container image)", g.Name)
}

// WaitCloudInit waits for cloud-init to finish, if the image has it. Its result is ignored, as before.
func (g *Guest) WaitCloudInit() {
	_, _ = g.S.Exec(g.Name, incus.ExecOpts{Cmd: []string{"sh", "-c",
		"if command -v cloud-init >/dev/null; then cloud-init status --wait >/dev/null 2>&1; fi"}})
}

// ProvisionNixOS writes /etc/nixos (configuration.nix, workstation-guest.nix,
// the machine's modules) and runs nixos-rebuild switch in the guest.
func (g *Guest) ProvisionNixOS(n *manifest.NixOS) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	subst := func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("@NAME@"), []byte(g.Name)) }
	for _, f := range []struct{ src, dst string }{
		{n.Configuration, "nixos/configuration.nix"},
		{n.GuestModule, "nixos/workstation-guest.nix"},
	} {
		data, err := os.ReadFile(f.src)
		if err != nil {
			return err
		}
		if err := addFile(tw, f.dst, subst(data), 0o644, 0); err != nil {
			return err
		}
	}
	if err := addDir(tw, "nixos/modules", 0); err != nil {
		return err
	}
	for _, m := range n.Modules {
		var err error
		if m.IsDir {
			err = addTree(tw, m.Src, fmt.Sprintf("nixos/modules/%d", m.Idx), 0)
		} else {
			err = addPath(tw, m.Src, fmt.Sprintf("nixos/modules/%d.nix", m.Idx), 0)
		}
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := g.run(incus.ExecOpts{Cmd: []string{"tar", "-xf", "-", "-C", "/etc"}, Stdin: &buf}); err != nil {
		return err
	}

	sh := nixosBin + "bash"
	// Restricted guests get no DNS from the host, and the rebuild below runs before the guest's
	// own `networking.nameservers` applies: seed resolv.conf (activation then takes it over).
	// A symlinked resolv.conf belongs to a resolver daemon and is left alone.
	if len(n.Nameservers) > 0 {
		var conf strings.Builder
		for _, ns := range n.Nameservers {
			fmt.Fprintf(&conf, "nameserver %s\n", ns)
		}
		if err := g.run(incus.ExecOpts{
			Cmd:   []string{sh, "-c", `[ -L /etc/resolv.conf ] || cat > /etc/resolv.conf`},
			Stdin: strings.NewReader(conf.String()),
		}); err != nil {
			return err
		}
	}
	channel := fmt.Sprintf("nix-instantiate --find-file nixpkgs >/dev/null 2>&1 || { nix-channel --add https://channels.nixos.org/%s nixos && nix-channel --update; }", n.Channel)
	if err := g.run(incus.ExecOpts{Cmd: []string{sh, "-lc", channel}}); err != nil {
		return err
	}
	return g.run(incus.ExecOpts{Cmd: []string{sh, "-lc", "nixos-rebuild switch"}})
}

// RunInit pushes the init script and runs it as root.
func (g *Guest) RunInit(script string) error {
	if script == "" {
		return nil
	}
	data, err := os.ReadFile(script)
	if err != nil {
		return err
	}
	if err := g.S.PushFile(g.Name, "/root/vm-init", data, 0, 0, 0o700); err != nil {
		return fmt.Errorf("%s: push init script: %w", g.Name, err)
	}
	return g.run(incus.ExecOpts{
		Cmd: []string{"/root/vm-init"},
		Env: map[string]string{"VM_NAME": g.Name, "VM_USER": g.User, "VM_HOME": g.Home},
	})
}

// ApplyDotfiles copies dir into the user's home. It is unpacked as the user, so
// symlinks the guest planted can't redirect the writes outside what it may touch.
func (g *Guest) ApplyDotfiles(dir string) error {
	if dir == "" {
		return nil
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := addTreeContents(tw, dir, "", uid); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return g.run(g.asUser(incus.ExecOpts{
		Cmd:   []string{"tar", "-xf", "-", "-C", g.Home, "--no-same-owner", "--no-same-permissions", "--overwrite"},
		Stdin: &buf,
	}))
}

// ApplyHome ships the host-built Home Manager closure into the guest's store
// over the agent (no guest network needed) and activates it as the user.
func (g *Guest) ApplyHome(activation string) error {
	if activation == "" {
		return nil
	}
	out, err := exec.Command(bin.NixStore, "-qR", activation).Output()
	if err != nil {
		return fmt.Errorf("nix-store -qR %s: %w", activation, err)
	}
	missing, err := g.missingPaths(strings.Fields(string(out)))
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		if err := g.importPaths(missing); err != nil {
			return err
		}
	}
	return g.run(g.asUser(incus.ExecOpts{Cmd: []string{activation + "/activate"}}))
}

// missingPaths keeps the order of paths (`-qR` lists references first, which import needs).
func (g *Guest) missingPaths(paths []string) ([]string, error) {
	invalid := map[string]bool{}
	for i := 0; i < len(paths); i += 500 {
		batch := paths[i:min(i+500, len(paths))]
		var stdout bytes.Buffer
		cmd := append([]string{nixosBin + "nix-store", "--check-validity", "--print-invalid"}, batch...)
		if err := g.run(incus.ExecOpts{Cmd: cmd, Stdout: &stdout}); err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(&stdout)
		for sc.Scan() {
			invalid[strings.TrimSpace(sc.Text())] = true
		}
	}
	var out []string
	for _, p := range paths {
		if invalid[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

func (g *Guest) importPaths(paths []string) error {
	export := exec.Command(bin.NixStore, append([]string{"--export"}, paths...)...)
	export.Stderr = os.Stderr
	stdout, err := export.StdoutPipe()
	if err != nil {
		return err
	}
	if err := export.Start(); err != nil {
		return fmt.Errorf("nix-store --export: %w", err)
	}
	importErr := g.run(incus.ExecOpts{Cmd: []string{nixosBin + "nix-store", "--import"}, Stdin: stdout, Stdout: io.Discard})
	if importErr != nil {
		// nobody reads the pipe any more: don't leave export blocked on it
		_ = export.Process.Kill()
		_ = export.Wait()
		return importErr
	}
	if err := export.Wait(); err != nil {
		return fmt.Errorf("nix-store --export: %w", err)
	}
	return nil
}

func addFile(tw *tar.Writer, name string, data []byte, mode int64, owner int) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: mode, Size: int64(len(data)), Uid: owner, Gid: owner, Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func addDir(tw *tar.Writer, name string, owner int) error {
	return tw.WriteHeader(&tar.Header{Name: name + "/", Mode: 0o755, Uid: owner, Gid: owner, Typeflag: tar.TypeDir})
}

// addPath adds a file or directory, following symlinks (like `cp -L`) and making
// store files writable for their owner (like the old `chmod -R u+w`).
func addPath(tw *tar.Writer, src, dst string, owner int) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return addTree(tw, src, dst, owner)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return addFile(tw, dst, data, int64(fi.Mode().Perm()|0o200), owner)
}

func addTree(tw *tar.Writer, src, dst string, owner int) error {
	if err := addDir(tw, dst, owner); err != nil {
		return err
	}
	return addTreeContents(tw, src, dst, owner)
}

func addTreeContents(tw *tar.Writer, src, dst string, owner int) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := addPath(tw, filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), owner); err != nil {
			return err
		}
	}
	return nil
}

// sanitized drops control characters from guest output, so a compromised guest
// can't drive the host terminal (clipboard, title, ...) through a provisioning log.
// It keeps newlines, tabs and carriage returns, and also drops C1 controls
// (U+0080-U+009F, 0xC2 0x80-0x9F in UTF-8), which some terminals honour.
type sanitized struct {
	w       io.Writer
	afterC2 bool
}

func (s *sanitized) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		if s.afterC2 {
			s.afterC2 = false
			if b >= 0x80 && b <= 0x9f {
				continue
			}
			out = append(out, 0xc2)
		}
		switch {
		case b == 0xc2:
			s.afterC2 = true
		case b == '\n' || b == '\t' || b == '\r':
			out = append(out, b)
		case b < 0x20 || b == 0x7f:
		default:
			out = append(out, b)
		}
	}
	if _, err := s.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
