package reconcile

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/lxc/incus/v7/shared/api"

	"sacabox/internal/guest"
	"sacabox/internal/incus"
	"sacabox/internal/manifest"
)

type Provisioner struct {
	S        incus.Server
	Manifest *manifest.Manifest
	Out      io.Writer
	// SSHAgent is set by interactive commands; systemd runs leave the agent device alone.
	SSHAgent SSHAgent
}

type Options struct {
	// NoSnapshot skips the `clean` snapshot of a fresh machine (throwaway machines).
	NoSnapshot bool
	// IfChanged skips machines that are up to date (see UpToDate).
	IfChanged bool
}

func (p *Provisioner) logf(format string, a ...any) { fmt.Fprintf(p.Out, format+"\n", a...) }

// Guest returns the guest side of machine name.
func (p *Provisioner) Guest(name string) *guest.Guest {
	return &guest.Guest{S: p.S, Name: name, User: p.Manifest.User, Home: p.Manifest.Home}
}

// Exists reports whether the instance exists.
func Exists(s incus.Server, name string) (bool, error) {
	_, _, err := s.GetInstance(name)
	if incus.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// Running reports whether the instance is running.
func Running(s incus.Server, name string) (bool, error) {
	inst, _, err := s.GetInstance(name)
	if err != nil {
		return false, err
	}
	return inst.StatusCode == api.Running, nil
}

// Provision creates the machine if needed, then makes it match its spec in
// every respect. Safe to run repeatedly. The network policy stays attached
// throughout, so running services are never cut off or opened up by an update.
func (p *Provisioner) Provision(name string, m *manifest.Machine, opts Options) error {
	inst, _, err := p.S.GetInstance(name)
	exists := err == nil
	if err != nil && !incus.IsNotFound(err) {
		return err
	}

	if exists {
		if bad := Forbidden(inst); len(bad) > 0 {
			return fmt.Errorf("%s: has settings sacabox refuses to run with: %s (unset them with `incus config unset`, or recreate the machine)",
				name, strings.Join(bad, ", "))
		}
		if opts.IfChanged {
			ok, err := p.UpToDate(name, m, inst)
			if err != nil {
				return err
			}
			if ok {
				p.logf("%s up to date", name)
				return nil
			}
		}
		if changes := Immutable(m, inst); len(changes) > 0 {
			if !m.RecreateOnChange {
				return fmt.Errorf("%s: %s can't change on an existing machine; set recreateOnChange = true to delete and recreate it (its root disk is lost), or `sacabox rm %s` first",
					name, joinChanges(changes), name)
			}
			if inst.Config[KeyManaged] != "true" {
				return fmt.Errorf("%s: %s can't change in place, and this instance wasn't created by sacabox, so it won't delete it; remove it yourself (`incus delete %s`)",
					name, joinChanges(changes), name)
			}
			p.logf("%s: recreating (%s)", name, joinChanges(changes))
			if err := p.Remove(name); err != nil {
				return err
			}
			exists = false
		}
	}

	// The ACL must exist before an instance can reference it.
	if err := p.ensureACL(name, WantACL(m)); err != nil {
		return err
	}
	fresh := !exists
	if fresh {
		if err := p.create(name, m); err != nil {
			return err
		}
	}
	if err := p.applyConfig(name, m); err != nil {
		return err
	}

	g := p.Guest(name)
	if running, err := Running(p.S, name); err != nil {
		return err
	} else if !running {
		if err := p.S.SetState(name, "start", false); err != nil {
			return fmt.Errorf("%s: start: %w", name, err)
		}
	}
	if err := g.WaitAgent(); err != nil {
		return err
	}

	if m.NixOS != nil {
		if err := g.ProvisionNixOS(m.NixOS); err != nil {
			return err
		}
	} else {
		g.WaitCloudInit()
	}
	if err := g.RunInit(m.Init); err != nil {
		return err
	}
	if err := g.ApplyDotfiles(m.Dotfiles); err != nil {
		return err
	}
	if err := g.ApplyHome(m.Home); err != nil {
		return err
	}

	if fresh && m.Snapshot && !opts.NoSnapshot {
		if err := p.S.CreateSnapshot(name, "clean"); err != nil {
			return fmt.Errorf("%s: snapshot: %w", name, err)
		}
	}
	if m.Hash != "" {
		if err := p.setConfig(name, KeySpecHash, m.Hash); err != nil {
			return err
		}
	}
	p.logf("%s provisioned", name)
	return nil
}

// UpToDate reports whether a machine was provisioned with this exact spec, is
// running, and nothing has drifted since: config, devices, ACL attachment and rules.
func (p *Provisioner) UpToDate(name string, m *manifest.Machine, inst *api.Instance) (bool, error) {
	if m.Hash == "" || inst.Config[KeySpecHash] != m.Hash || inst.StatusCode != api.Running {
		return false, nil
	}
	if _, changes, _ := Plan(name, m, inst.Writable(), SSHAgent{}); len(changes) > 0 {
		return false, nil
	}
	if want := WantACL(m); want != nil {
		cur, _, err := p.S.GetNetworkACL(ACLName(name))
		if incus.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if ACLDiffers(cur, want) {
			return false, nil
		}
	}
	return true, nil
}

// Reset rolls the machine back to its `clean` snapshot. The snapshot may predate
// the current spec (policy, shares, ssh-agent), so that is re-applied before it boots.
func (p *Provisioner) Reset(name string, m *manifest.Machine) error {
	inst, _, err := p.S.GetInstance(name)
	if err != nil {
		return err
	}
	if inst.StatusCode == api.Running {
		if err := p.S.SetState(name, "stop", true); err != nil {
			return fmt.Errorf("%s: stop: %w", name, err)
		}
	}
	// same request as `incus snapshot restore`: with Restore set, Incus ignores the other fields
	if err := p.S.UpdateInstance(name, api.InstancePut{Restore: "clean"}, ""); err != nil {
		return fmt.Errorf("%s: restore: %w", name, err)
	}
	if m != nil {
		if inst, _, err = p.S.GetInstance(name); err != nil {
			return err
		}
		if bad := Forbidden(inst); len(bad) > 0 {
			return fmt.Errorf("%s: snapshot has settings sacabox refuses to run with: %s", name, strings.Join(bad, ", "))
		}
		if err := p.ensureACL(name, WantACL(m)); err != nil {
			return err
		}
		if err := p.applyConfig(name, m); err != nil {
			return err
		}
	}
	if err := p.S.SetState(name, "start", false); err != nil {
		return fmt.Errorf("%s: start: %w", name, err)
	}
	return nil
}

func joinChanges(cs []Change) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.String()
	}
	return strings.Join(parts, "; ")
}

func (p *Provisioner) create(name string, m *manifest.Machine) error {
	profile, err := p.S.GetProfile("default")
	if err != nil {
		return fmt.Errorf("default profile: %w", err)
	}
	var cloudInit string
	if m.CloudInit != "" {
		data, err := os.ReadFile(m.CloudInit)
		if err != nil {
			return err
		}
		cloudInit = string(data)
	}
	var cloudNetwork string
	if m.CloudNetwork != "" {
		data, err := os.ReadFile(m.CloudNetwork)
		if err != nil {
			return err
		}
		cloudNetwork = string(data)
	}
	req, err := CreateRequest(name, m, profile, cloudInit, cloudNetwork)
	if err != nil {
		return err
	}
	p.logf("creating %s from %s", name, m.Image)
	if err := p.S.Create(m.Image, req); err != nil {
		return fmt.Errorf("%s: create: %w", name, err)
	}
	return nil
}

// update applies f to the instance's current config and writes it back if it changed.
func (p *Provisioner) update(name string, f func(api.InstancePut) api.InstancePut) error {
	inst, etag, err := p.S.GetInstance(name)
	if err != nil {
		return err
	}
	cur := inst.Writable()
	want := f(clonePut(cur))
	if len(Diff(cur, want)) == 0 {
		return nil
	}
	if err := p.S.UpdateInstance(name, want, etag); err != nil {
		return fmt.Errorf("%s: update: %w", name, err)
	}
	return nil
}

// clonePut copies the maps too, so f can edit freely without touching cur.
func clonePut(put api.InstancePut) api.InstancePut {
	out := put
	out.Config = maps.Clone(put.Config)
	if out.Config == nil {
		out.Config = map[string]string{}
	}
	out.Devices = make(map[string]map[string]string, len(put.Devices))
	for k, d := range put.Devices {
		out.Devices[k] = maps.Clone(d)
	}
	return out
}

func (p *Provisioner) applyConfig(name string, m *manifest.Machine) error {
	for _, sh := range m.Shares {
		if err := CheckShare(sh.Host, p.Manifest.AllowedShareRoots); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	var planErr error
	if err := p.update(name, func(cur api.InstancePut) api.InstancePut {
		want, _, warnings := Plan(name, m, cur, p.SSHAgent)
		if m.Network.ACL != nil && !ACLAttached(want.Devices, name) {
			planErr = fmt.Errorf("%s: eth0 isn't a device of the instance itself, so its network ACL can't be attached; recreate the machine", name)
			return cur
		}
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "sacabox: warning: %s: %s\n", name, w)
		}
		return want
	}); err != nil {
		return err
	}
	if planErr != nil {
		return planErr
	}
	// No longer referenced once the policy dropped its ACL (now open).
	if m.Network.ACL == nil {
		if err := p.S.DeleteNetworkACL(ACLName(name)); err != nil && !incus.IsNotFound(err) {
			return fmt.Errorf("%s: delete ACL: %w", name, err)
		}
	}
	return nil
}

// CheckShare refuses a share source that isn't a plain directory at the declared
// path: a guest with a writable share could swap a directory for a symlink to
// anywhere on the host. Symlinks above the allowed root (e.g. /home -> /var/home)
// are fine; anything below it must be real. Also re-checks allowedShareRoots.
func CheckShare(host string, roots []string) error {
	host = filepath.Clean(host)
	expected := host
	if len(roots) > 0 {
		var root string
		for _, r := range roots {
			r = filepath.Clean(r)
			if host == r || strings.HasPrefix(host, r+"/") {
				root = r
				break
			}
		}
		if root == "" {
			return fmt.Errorf("share source %s is outside allowedShareRoots (%s)", host, strings.Join(roots, " "))
		}
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return fmt.Errorf("share root %s: %w", root, err)
		}
		expected = realRoot + strings.TrimPrefix(host, root)
	}
	resolved, err := filepath.EvalSymlinks(host)
	if err != nil {
		return fmt.Errorf("share source %s does not exist", host)
	}
	if resolved != expected {
		return fmt.Errorf("share source %s resolves to %s through a symlink; refusing", host, resolved)
	}
	if fi, err := os.Stat(resolved); err != nil || !fi.IsDir() {
		return fmt.Errorf("share source %s is not a directory", host)
	}
	return nil
}

func (p *Provisioner) setConfig(name, key, value string) error {
	return p.update(name, func(cur api.InstancePut) api.InstancePut {
		cur.Config[key] = value
		return cur
	})
}

func (p *Provisioner) ensureACL(name string, want *api.NetworkACLPut) error {
	if want == nil {
		return nil
	}
	acl := ACLName(name)
	cur, etag, err := p.S.GetNetworkACL(acl)
	if incus.IsNotFound(err) {
		err = p.S.CreateNetworkACL(api.NetworkACLsPost{NetworkACLPost: api.NetworkACLPost{Name: acl}, NetworkACLPut: *want})
		if err != nil {
			return fmt.Errorf("%s: create ACL: %w", name, err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !ACLDiffers(cur, want) {
		return nil
	}
	if err := p.S.UpdateNetworkACL(acl, *want, etag); err != nil {
		return fmt.Errorf("%s: update ACL: %w", name, err)
	}
	return nil
}

// Remove deletes the instance (stopping it first) and its network ACL.
func (p *Provisioner) Remove(name string) error {
	inst, _, err := p.S.GetInstance(name)
	if err != nil && !incus.IsNotFound(err) {
		return err
	}
	if err == nil {
		if inst.StatusCode == api.Running {
			if err := p.S.SetState(name, "stop", true); err != nil {
				return fmt.Errorf("%s: stop: %w", name, err)
			}
		}
		if err := p.S.Delete(name); err != nil {
			return fmt.Errorf("%s: delete: %w", name, err)
		}
	}
	if err := p.S.DeleteNetworkACL(ACLName(name)); err != nil && !incus.IsNotFound(err) {
		return fmt.Errorf("%s: delete ACL: %w", name, err)
	}
	return nil
}
