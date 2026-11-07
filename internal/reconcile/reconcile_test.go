package reconcile

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"

	"sacabox/internal/manifest"
)

func machine() *manifest.Machine {
	return &manifest.Machine{
		OS: "generic", Type: "vm", Image: "images:debian/13/cloud", Snapshot: true,
		CPU: 2, Memory: "4GiB", Disk: "30GiB",
		Network: manifest.Network{Policy: "internet", Bridge: "incusbr0", ACL: &manifest.ACL{
			Egress: []api.NetworkACLRule{{Action: "allow", Destination: "8.0.0.0/7", State: "enabled"}},
		}},
		Hash: "h1",
	}
}

func provisioner(f *fake) *Provisioner {
	return &Provisioner{S: f, Manifest: &manifest.Manifest{User: "you", Home: "/home/you"}, Out: io.Discard}
}

func TestProvisionFresh(t *testing.T) {
	f := newFake()
	if err := provisioner(f).Provision("box", machine(), Options{}); err != nil {
		t.Fatal(err)
	}
	inst := f.instances["box"]
	if inst.Type != "virtual-machine" || inst.StatusCode != api.Running {
		t.Fatalf("instance %s %v", inst.Type, inst.StatusCode)
	}
	for k, v := range map[string]string{KeyManaged: "true", KeySpecHash: "h1", KeyOS: "generic", "limits.cpu": "2", "security.secureboot": "false"} {
		if inst.Config[k] != v {
			t.Errorf("config %s = %q, want %q", k, inst.Config[k], v)
		}
	}
	if got := inst.Devices["root"]["size"]; got != "30GiB" {
		t.Errorf("root size %q", got)
	}
	if got := inst.Devices["eth0"]["security.acls"]; got != "ws-box" {
		t.Errorf("ACL not attached at the end: %q", got)
	}
	if _, ok := f.acls["ws-box"]; !ok {
		t.Error("ACL ws-box not created")
	}
	// The policy is never lifted, not even while provisioning.
	for cmd, acl := range f.aclDuringExec {
		if acl != "ws-box" {
			t.Errorf("%s ran with security.acls=%q", cmd, acl)
		}
	}
	if !slices.Equal(f.snapshots, []string{"box/clean acl=ws-box"}) {
		t.Errorf("snapshots %v", f.snapshots)
	}
}

func TestProvisionFailureKeepsPolicy(t *testing.T) {
	f := newFake()
	m := machine()
	m.Init = writeTemp(t, "#!/bin/sh\nexit 1\n")
	f.failExec = "/root/vm-init"
	err := provisioner(f).Provision("box", m, Options{})
	if err == nil {
		t.Fatal("expected the failing init script to fail provisioning")
	}
	if got := f.instances["box"].Devices["eth0"]["security.acls"]; got != "ws-box" {
		t.Fatalf("guest unrestricted after a failure: security.acls=%q", got)
	}
	if f.instances["box"].Config[KeySpecHash] != "" {
		t.Error("spec hash recorded for a failed provision")
	}
}

func TestCreatedWithPolicy(t *testing.T) {
	m := machine()
	profile, _ := newFake().GetProfile("default")
	req, err := CreateRequest("box", m, profile, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ACLAttached(req.Devices, "box") {
		t.Errorf("instance not created with its ACL: %v", req.Devices["eth0"])
	}
	for _, k := range []string{"security.mac_filtering", "security.ipv4_filtering", "security.ipv6_filtering"} {
		if req.Devices["eth0"][k] != "true" {
			t.Errorf("created without %s", k)
		}
	}
}

func TestFilteringOnExistingOpenMachine(t *testing.T) {
	m := machine()
	m.Network = manifest.Network{Policy: "open", Bridge: "incusbr0"}
	cur := api.InstancePut{Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0"}}}
	want, _, _ := Plan("box", m, cur, SSHAgent{})
	if want.Devices["eth0"]["security.ipv4_filtering"] != "true" || want.Devices["eth0"]["security.mac_filtering"] != "true" {
		t.Errorf("spoofing filters not added to an open machine: %v", want.Devices["eth0"])
	}

	m.Network = manifest.Network{Policy: "none"}
	cur = api.InstancePut{Devices: map[string]map[string]string{"eth0": {"type": "none"}}}
	if want, _, _ := Plan("box", m, cur, SSHAgent{}); len(want.Devices["eth0"]) != 1 {
		t.Errorf("filters added to a none device: %v", want.Devices["eth0"])
	}
}

func TestUpToDateNoticesDrift(t *testing.T) {
	f := newFake()
	p := provisioner(f)
	if err := p.Provision("box", machine(), Options{}); err != nil {
		t.Fatal(err)
	}
	// Someone detaches the ACL by hand: same spec hash, but not up to date.
	delete(f.instances["box"].Devices["eth0"], "security.acls")
	if ok, _ := p.UpToDate("box", machine(), mustGet(t, f, "box")); ok {
		t.Fatal("detached ACL reported as up to date")
	}
	if err := p.Provision("box", machine(), Options{IfChanged: true}); err != nil {
		t.Fatal(err)
	}
	if !ACLAttached(f.instances["box"].Devices, "box") {
		t.Error("reconcile didn't reattach the ACL")
	}
	// Loosened rules in the ACL itself are drift too.
	f.acls["ws-box"].Egress = append(f.acls["ws-box"].Egress, api.NetworkACLRule{Action: "allow", Destination: "0.0.0.0/0", State: "enabled"})
	if ok, _ := p.UpToDate("box", machine(), mustGet(t, f, "box")); ok {
		t.Error("changed ACL rules reported as up to date")
	}
}

func TestNestingReconciled(t *testing.T) {
	f := newFake()
	p := provisioner(f)
	m := machine()
	m.Type, m.Nesting = "container", true
	if err := p.Provision("ct", m, Options{}); err != nil {
		t.Fatal(err)
	}
	if f.instances["ct"].Config["security.nesting"] != "true" {
		t.Fatal("nesting not set")
	}
	m.Nesting = false
	if err := p.Provision("ct", m, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.instances["ct"].Config["security.nesting"]; ok {
		t.Error("security.nesting kept after nesting = false")
	}
}

func TestForbiddenSettings(t *testing.T) {
	f := newFake()
	p := provisioner(f)
	if err := p.Provision("box", machine(), Options{}); err != nil {
		t.Fatal(err)
	}
	f.instances["box"].Config["security.privileged"] = "true"
	f.instances["box"].Config["raw.lxc"] = "lxc.apparmor.profile=unconfined"
	err := p.Provision("box", machine(), Options{})
	if err == nil || !strings.Contains(err.Error(), "raw.lxc, security.privileged=true") {
		t.Fatalf("err = %v", err)
	}
}

func TestRecreateOnlyManaged(t *testing.T) {
	f := newFake()
	f.instances["box"] = &api.Instance{Name: "box", Type: "container", InstancePut: api.InstancePut{
		Config: map[string]string{}, Devices: map[string]map[string]string{},
	}}
	m := machine()
	m.RecreateOnChange = true
	err := provisioner(f).Provision("box", m, Options{})
	if err == nil || !strings.Contains(err.Error(), "wasn't created by sacabox") {
		t.Fatalf("err = %v", err)
	}
	if f.instances["box"] == nil {
		t.Fatal("unmanaged instance deleted")
	}
}

func TestResetReappliesSpec(t *testing.T) {
	f := newFake()
	p := provisioner(f)
	m := machine()
	m.Network = manifest.Network{Policy: "open", Bridge: "incusbr0"}
	m.SSHAgent = true
	p.SSHAgent = SSHAgent{Manage: true, Socket: "/run/user/1000/agent"}
	if err := p.Provision("box", m, Options{}); err != nil {
		t.Fatal(err)
	}
	// The clean snapshot is open with an agent; the spec has since been locked down.
	locked := machine()
	if err := p.Reset("box", locked); err != nil {
		t.Fatal(err)
	}
	inst := f.instances["box"]
	if !ACLAttached(inst.Devices, "box") {
		t.Error("reset booted without the current policy")
	}
	if _, ok := inst.Devices["ssh-agent"]; ok {
		t.Error("reset brought back the ssh-agent proxy")
	}
	if inst.StatusCode != api.Running {
		t.Error("not started after reset")
	}
}

func TestCheckShare(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	must(t, os.MkdirAll(filepath.Join(root, "real"), 0o755))
	must(t, os.Symlink("/", filepath.Join(root, "evil")))
	// a symlink above the root, like /home -> /var/home
	linkedBase := filepath.Join(t.TempDir(), "home")
	must(t, os.Symlink(base, linkedBase))

	for _, tc := range []struct {
		host  string
		roots []string
		ok    bool
	}{
		{filepath.Join(root, "real"), []string{root}, true},
		{filepath.Join(root, "real"), nil, true},
		{filepath.Join(root, "evil"), []string{root}, false},
		{filepath.Join(root, "evil"), nil, false},
		{filepath.Join(root, "missing"), []string{root}, false},
		{filepath.Join(base, "other"), []string{root}, false},
		{filepath.Join(linkedBase, "projects", "real"), []string{filepath.Join(linkedBase, "projects")}, true},
		{filepath.Join(linkedBase, "projects", "evil"), []string{filepath.Join(linkedBase, "projects")}, false},
	} {
		if err := CheckShare(tc.host, tc.roots); (err == nil) != tc.ok {
			t.Errorf("CheckShare(%s, %v) = %v, want ok=%v", tc.host, tc.roots, err, tc.ok)
		}
	}
}

func mustGet(t *testing.T, f *fake, name string) *api.Instance {
	t.Helper()
	inst, _, err := f.GetInstance(name)
	must(t, err)
	return inst
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestReconcileSkipsUpToDate(t *testing.T) {
	f := newFake()
	p := provisioner(f)
	if err := p.Provision("box", machine(), Options{}); err != nil {
		t.Fatal(err)
	}
	f.execs = nil
	if err := p.Provision("box", machine(), Options{IfChanged: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.execs) != 0 {
		t.Errorf("up-to-date machine was re-provisioned: %v", f.execs)
	}
	m := machine()
	m.Hash, m.CPU = "h2", 4
	if err := p.Provision("box", m, Options{IfChanged: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.instances["box"].Config["limits.cpu"]; got != "4" {
		t.Errorf("limits.cpu = %q after spec change", got)
	}
	if len(f.snapshots) != 1 {
		t.Errorf("snapshot retaken on an existing machine: %v", f.snapshots)
	}
}

func TestImmutableChange(t *testing.T) {
	f := newFake()
	p := provisioner(f)
	if err := p.Provision("box", machine(), Options{}); err != nil {
		t.Fatal(err)
	}
	m := machine()
	m.Type = "container"
	err := p.Provision("box", m, Options{})
	if err == nil || !strings.Contains(err.Error(), "recreateOnChange") {
		t.Fatalf("type change: err = %v", err)
	}
	if f.instances["box"].Type != "virtual-machine" {
		t.Fatal("instance changed without recreateOnChange")
	}

	m.RecreateOnChange = true
	if err := p.Provision("box", m, Options{}); err != nil {
		t.Fatal(err)
	}
	if f.instances["box"].Type != "container" {
		t.Fatal("instance not recreated")
	}
}

func TestPolicyOpenRemovesACL(t *testing.T) {
	f := newFake()
	p := provisioner(f)
	if err := p.Provision("box", machine(), Options{}); err != nil {
		t.Fatal(err)
	}
	m := machine()
	m.Network = manifest.Network{Policy: "open", Bridge: "incusbr0"}
	if err := p.Provision("box", m, Options{}); err != nil {
		t.Fatal(err)
	}
	eth0 := f.instances["box"].Devices["eth0"]
	for _, k := range []string{"security.acls", "security.acls.default.egress.action", "security.acls.default.ingress.action"} {
		if _, ok := eth0[k]; ok {
			t.Errorf("eth0 still has %s", k)
		}
	}
	if _, ok := f.acls["ws-box"]; ok {
		t.Error("ACL ws-box not deleted")
	}
}

func TestPlanShares(t *testing.T) {
	m := machine()
	m.Shares = []manifest.Share{{Name: "code", Host: "/home/you/code", Guest: "/code"}}
	cur := api.InstancePut{
		Config: map[string]string{},
		Devices: map[string]map[string]string{
			"root":           {"type": "disk", "path": "/", "pool": "default", "size": "30GiB"},
			"ws-share-old":   {"type": "disk", "source": "/home/you/old", "path": "/old"},
			"ws-share-code":  {"type": "disk", "source": "/home/you/code", "path": "/code"},
			"unrelated-disk": {"type": "disk", "source": "/srv", "path": "/srv"},
		},
	}
	want, changes, _ := Plan("box", m, cur, SSHAgent{})
	if _, ok := want.Devices["ws-share-old"]; ok {
		t.Error("undeclared share kept")
	}
	if _, ok := want.Devices["unrelated-disk"]; !ok {
		t.Error("device not managed by sacabox removed")
	}
	// readonly unset == false: an equal share is left untouched.
	for _, c := range changes {
		if c.What == "device ws-share-code" {
			t.Errorf("unchanged share rewritten: %s", c)
		}
	}
}

func TestPlanRootShrink(t *testing.T) {
	m := machine()
	m.Disk = "10GiB"
	cur := api.InstancePut{Devices: map[string]map[string]string{"root": {"type": "disk", "size": "30GiB"}}}
	want, _, warnings := Plan("box", m, cur, SSHAgent{})
	if want.Devices["root"]["size"] != "30GiB" || len(warnings) != 1 {
		t.Errorf("shrink: size %q, warnings %v", want.Devices["root"]["size"], warnings)
	}
}

func TestPlanSSHAgent(t *testing.T) {
	m := machine()
	cur := api.InstancePut{Devices: map[string]map[string]string{"ssh-agent": {"type": "proxy"}}}

	// Not wanted: removed even by a non-interactive run.
	want, _, _ := Plan("box", m, cur, SSHAgent{})
	if _, ok := want.Devices["ssh-agent"]; ok {
		t.Error("ssh-agent kept although sshAgent = false")
	}

	// Wanted, non-interactive: left as it is.
	m.SSHAgent = true
	want, _, _ = Plan("box", m, cur, SSHAgent{})
	if want.Devices["ssh-agent"]["type"] != "proxy" || len(want.Devices["ssh-agent"]) != 1 {
		t.Error("systemd run touched the ssh-agent device")
	}

	// Wanted, interactive with a socket: forwarded.
	want, _, _ = Plan("box", m, cur, SSHAgent{Manage: true, Socket: "/run/user/1000/agent", UID: 1000, GID: 100})
	if got := want.Devices["ssh-agent"]["connect"]; got != "unix:/run/user/1000/agent" {
		t.Errorf("connect = %q", got)
	}
}

func TestACLDiffers(t *testing.T) {
	want := WantACL(machine())
	cur := &api.NetworkACL{NetworkACLPut: *want}
	cur.Ingress = nil
	if ACLDiffers(cur, want) {
		t.Error("nil and empty rule lists treated as different")
	}
	cur.Egress = nil
	if !ACLDiffers(cur, want) {
		t.Error("missing egress rule not detected")
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/script"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRestrictedBridgeAndResolvers(t *testing.T) {
	// An open machine becomes restricted: its NIC moves to the restricted bridge.
	m := machine()
	m.Network.Bridge = "incusbr1"
	cur := api.InstancePut{Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0"}}}
	want, _, warnings := Plan("box", m, cur, SSHAgent{})
	if want.Devices["eth0"]["network"] != "incusbr1" || len(warnings) == 0 {
		t.Errorf("eth0 not moved to the restricted bridge (warnings %v)", warnings)
	}

	// Generic guests get their resolvers through cloud-init's network config at creation.
	profile, _ := newFake().GetProfile("default")
	req, err := CreateRequest("box", m, profile, "", "version: 2\n")
	if err != nil {
		t.Fatal(err)
	}
	if req.Config["cloud-init.network-config"] != "version: 2\n" || req.Devices["eth0"]["network"] != "incusbr1" {
		t.Errorf("config %v, eth0 %v", req.Config, req.Devices["eth0"])
	}
}
