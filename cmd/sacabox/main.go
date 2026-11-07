// The `sacabox` command. Everything declarative comes from /etc/sacabox/machines.json
// (generated from services.sacabox.machines / templates); this only reconciles Incus with it.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"sacabox/internal/bin"
	"sacabox/internal/incus"
	"sacabox/internal/lock"
	"sacabox/internal/manifest"
	"sacabox/internal/reconcile"
)

const usage = `sacabox sh NAME [cmd...]   shell as your user (creates + provisions the machine on first use)
sacabox term NAME          same, in a dedicated terminal window (services.sacabox.terminal.enable)
sacabox provision NAME     apply the config files to the machine, creating it if missing:
                           refresh flake inputs + rebuild the host (services.sacabox.flake),
                           then resources, shares, network policy, guest system, init, dotfiles,
                           home. --no-rebuild skips the host rebuild.
sacabox reconcile NAME|--all  like provision --no-rebuild, but skips machines already up to date
sacabox plan NAME|--all    show what provision/reconcile would change, without changing anything
sacabox root NAME          root shell
sacabox ls                 machines (running and declared) with their IPs
sacabox reset NAME         roll back to the pristine ` + "`clean`" + ` snapshot taken after first provisioning
sacabox stop NAME
sacabox rm NAME            delete the machine and its network ACL
sacabox tmp [TEMPLATE|MACHINE] [--ct]   throwaway machine from a template, deleted on exit
`

// The setuid wrapper, never whatever `sudo` comes first in $PATH.
const sudo = "/run/wrappers/bin/sudo"

// exitError carries a command's exit status through without an error message.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func main() {
	err := run(os.Args[1:])
	var code exitError
	switch {
	case err == nil:
	case errors.As(err, &code):
		os.Exit(int(code))
	default:
		fmt.Fprintf(os.Stderr, "sacabox: %s\n", err)
		os.Exit(1)
	}
}

type app struct {
	m *manifest.Manifest
	s incus.Server
	p *reconcile.Provisioner
}

func newApp() (*app, error) {
	path := os.Getenv("SACABOX_MANIFEST")
	if path == "" {
		path = manifest.DefaultPath
	}
	m, err := manifest.Load(path)
	if err != nil {
		return nil, err
	}
	s, err := incus.Connect()
	if err != nil {
		return nil, err
	}
	agent := reconcile.SSHAgent{Manage: true, UID: os.Getuid(), GID: os.Getgid()}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if fi, err := os.Stat(sock); err == nil && fi.Mode()&os.ModeSocket != 0 {
			agent.Socket = sock
		}
	}
	return &app{m: m, s: s, p: &reconcile.Provisioner{S: s, Manifest: m, Out: os.Stdout, SSHAgent: agent}}, nil
}

func run(args []string) error {
	cmd := "help"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "sh", "term", "provision", "reconcile", "plan", "root", "reset", "stop", "rm":
		if len(args) < 1 {
			fmt.Fprint(os.Stderr, usage)
			return exitError(2)
		}
		if !lock.ValidName(args[0]) && !(args[0] == "--all" && (cmd == "reconcile" || cmd == "plan")) {
			return fmt.Errorf("invalid machine name %q", args[0])
		}
	case "ls", "tmp":
	default:
		fmt.Fprint(os.Stderr, usage)
		return exitError(2)
	}

	a, err := newApp()
	if err != nil {
		return err
	}
	switch cmd {
	case "sh":
		return a.shell(args[0], args[1:])
	case "term":
		if !a.m.Terminal.Enable {
			fmt.Fprint(os.Stderr, usage)
			return exitError(2)
		}
		return a.term(args[0])
	case "provision":
		return a.provisionCmd(args[0], len(args) > 1 && args[1] == "--no-rebuild")
	case "reconcile":
		return a.forEach(args[0], func(n string, m *manifest.Machine) error {
			return a.provision(n, m, reconcile.Options{IfChanged: true})
		})
	case "plan":
		return a.forEach(args[0], a.plan)
	case "root":
		return a.root(args[0])
	case "ls":
		if err := runCmd(bin.Incus, "list", "-c", "ntsS4"); err != nil {
			return err
		}
		fmt.Printf("\n%s\n", a.m.DeclaredSummary())
		return nil
	case "reset":
		return a.reset(args[0])
	case "stop":
		return a.s.SetState(args[0], "stop", false)
	case "rm":
		release, err := lock.Acquire(args[0])
		if err != nil {
			return err
		}
		defer release()
		return a.p.Remove(args[0])
	case "tmp":
		return a.tmp(args)
	}
	return nil
}

func (a *app) forEach(target string, f func(string, *manifest.Machine) error) error {
	names := []string{target}
	if target == "--all" {
		names = a.m.MachineNames()
	}
	var errs []error
	for _, n := range names {
		m, err := a.m.Machine(n)
		if err == nil {
			err = f(n, m)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "sacabox: %s\n", err)
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return exitError(1)
	}
	return nil
}

func (a *app) provision(name string, m *manifest.Machine, opts reconcile.Options) error {
	release, err := lock.Acquire(name)
	if err != nil {
		return err
	}
	defer release()
	return a.p.Provision(name, m, opts)
}

func (a *app) provisionCmd(name string, noRebuild bool) error {
	m, err := a.m.Machine(name)
	if err != nil {
		return err
	}
	if !noRebuild && a.m.Flake.Path != "" {
		if err := a.rebuild(); err != nil {
			return err
		}
		// the rebuild may have replaced this very binary and the manifest: continue with the new ones
		return syscall.Exec("/run/current-system/sw/bin/sacabox",
			[]string{"sacabox", "provision", name, "--no-rebuild"}, os.Environ())
	}
	return a.provision(name, m, reconcile.Options{})
}

// rebuild refreshes the configured flake inputs and rebuilds the host so the manifest is current.
func (a *app) rebuild() error {
	f := a.m.Flake
	if len(f.Inputs) > 0 {
		tmp, err := os.CreateTemp("", "sacabox-lock-")
		if err != nil {
			return err
		}
		tmp.Close()
		defer os.Remove(tmp.Name())
		args := append([]string{"flake", "update"}, f.Inputs...)
		args = append(args, "--flake", "path:"+f.Path, "--output-lock-file", tmp.Name())
		if err := runCmd(bin.Nix, args...); err != nil {
			return err
		}
		if err := runCmd(sudo, "cp", tmp.Name(), f.Path+"/flake.lock"); err != nil {
			return err
		}
	}
	return runCmd(sudo, "/run/current-system/sw/bin/nixos-rebuild", "switch", "--flake", f.Path+"#"+f.Host)
}

func (a *app) plan(name string, m *manifest.Machine) error {
	inst, _, err := a.s.GetInstance(name)
	if incus.IsNotFound(err) {
		fmt.Printf("%s: create from %s, then provision\n", name, m.Image)
		return nil
	}
	if err != nil {
		return err
	}
	if bad := reconcile.Forbidden(inst); len(bad) > 0 {
		fmt.Printf("%s: error, has settings sacabox refuses to run with: %s\n", name, strings.Join(bad, ", "))
		return nil
	}
	if changes := reconcile.Immutable(m, inst); len(changes) > 0 {
		verb := "error, can't change in place"
		if m.RecreateOnChange {
			verb = "recreate (root disk is lost)"
		}
		fmt.Printf("%s: %s:\n", name, verb)
		printChanges(changes)
		return nil
	}
	upToDate, err := a.p.UpToDate(name, m, inst)
	if err != nil {
		return err
	}
	if upToDate {
		fmt.Printf("%s: up to date\n", name)
		return nil
	}
	_, changes, warnings := reconcile.Plan(name, m, inst.Writable(), a.p.SSHAgent)
	if want := reconcile.WantACL(m); want != nil {
		cur, _, err := a.s.GetNetworkACL(reconcile.ACLName(name))
		switch {
		case incus.IsNotFound(err):
			changes = append(changes, reconcile.Change{What: "acl " + reconcile.ACLName(name), To: "create"})
		case err != nil:
			return err
		case reconcile.ACLDiffers(cur, want):
			changes = append(changes, reconcile.Change{What: "acl " + reconcile.ACLName(name), From: "rules", To: "new rules"})
		}
	}
	fmt.Printf("%s:\n", name)
	printChanges(changes)
	fmt.Println("  re-provision the guest")
	for _, w := range warnings {
		fmt.Printf("  warning: %s\n", w)
	}
	return nil
}

func printChanges(cs []reconcile.Change) {
	for _, c := range cs {
		fmt.Printf("  %s\n", c)
	}
}

func (a *app) ensure(name string) error {
	ok, err := reconcile.Exists(a.s, name)
	if err != nil || ok {
		return err
	}
	m, err := a.m.Machine(name)
	if err != nil {
		return err
	}
	return a.provision(name, m, reconcile.Options{})
}

func (a *app) start(name string) error {
	running, err := reconcile.Running(a.s, name)
	if err != nil || running {
		return err
	}
	if err := a.s.SetState(name, "start", false); err != nil {
		return err
	}
	return a.p.Guest(name).WaitAgent()
}

func (a *app) shell(name string, cmd []string) error {
	if err := a.ensure(name); err != nil {
		return err
	}
	if err := a.start(name); err != nil {
		return err
	}
	if len(cmd) == 0 {
		// the user's own login shell (fish, zsh, ...)
		cmd = []string{"sh", "-c", `exec "$(getent passwd 1000 | cut -d: -f7)" -l`}
	}
	var color string
	if m, ok := a.m.Machines[name]; ok {
		color = m.Color
	}
	if color != "" && isTerminal(os.Stdout) && os.Getenv("SACABOX_TERM") == "" {
		banner(name, color, "enter")
		defer banner(name, color, "leave")
	}
	term := os.Getenv("TERM")
	if term == "" {
		term = "xterm-256color"
	}
	h := a.m.Home
	args := append([]string{"exec", name, "--force-interactive", "--user", "1000", "--group", "1000",
		"--cwd", h, "--env", "HOME=" + h, "--env", "USER=" + a.m.User, "--env", "PATH=" + bin.GuestPath,
		"--env", "TERM=" + term, "--"}, cmd...)
	return runInteractive(bin.Incus, args...)
}

func (a *app) root(name string) error {
	if err := a.ensure(name); err != nil {
		return err
	}
	return syscall.Exec(lookPath(bin.Incus), []string{"incus", "exec", name, "--env", "PATH=" + bin.GuestPath, "--",
		"sh", "-c", `exec "$(command -v bash || echo sh)" -l`}, os.Environ())
}

func (a *app) reset(name string) error {
	release, err := lock.Acquire(name)
	if err != nil {
		return err
	}
	defer release()
	// undeclared machines (tmp-*) are only rolled back
	return a.p.Reset(name, a.m.Machines[name])
}

func (a *app) tmp(args []string) error {
	src, ct := "dev", false
	for _, arg := range args {
		if arg == "--ct" {
			ct = true
		} else {
			src = arg
		}
	}
	var spec manifest.Machine
	if t, ok := a.m.Templates[src]; ok {
		spec = *t
	} else {
		m, err := a.m.Machine(src)
		if err != nil {
			return err
		}
		spec = *m
	}
	if ct {
		spec.Type = "container"
	}
	// throwaway: never recorded as up to date
	spec.Hash = ""

	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	name := "tmp-" + hex.EncodeToString(b)

	// Clean up on Ctrl-C / hangup too, not only on a normal exit.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	cleanup := func() { _ = a.p.Remove(name) }
	go func() {
		if _, ok := <-sigs; ok {
			cleanup()
			os.Exit(130)
		}
	}()
	defer cleanup()

	if err := a.provision(name, &spec, reconcile.Options{NoSnapshot: true}); err != nil {
		return err
	}
	// the shell's exit status doesn't matter for a throwaway machine
	_ = a.shell(name, nil)
	return nil
}

func lookPath(file string) string {
	if p, err := exec.LookPath(file); err == nil {
		return p
	}
	return file
}

func runCmd(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// runInteractive runs a foreground command, passing its exit status through.
// Ctrl-C goes to the command (same terminal) and must not kill sacabox first.
func runInteractive(name string, args ...string) error {
	signal.Ignore(os.Interrupt, syscall.SIGQUIT)
	defer signal.Reset(os.Interrupt, syscall.SIGQUIT)
	err := runCmd(name, args...)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return exitError(ee.ExitCode())
	}
	return err
}
