// Package reconcile makes an Incus instance match its manifest spec.
//
// plan.go is pure: given a spec and the instance as Incus reports it, it computes
// what must change. provision.go applies that and provisions the guest.
package reconcile

import (
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/units"

	"sacabox/internal/manifest"
)

// Keys sacabox stores on the instances it manages.
const (
	KeyManaged  = "user.sacabox.managed"
	KeySpecHash = "user.sacabox.spec-hash"
	KeyOS       = "user.sacabox.os"
	KeyImage    = "user.sacabox.image"
)

const (
	sharePrefix = "ws-share-"
	sshAgentDev = "ssh-agent"
)

// ACLName is the network ACL holding a machine's policy (renaming it would orphan existing ACLs).
func ACLName(machine string) string { return "ws-" + machine }

// Change is one difference between the instance and its spec, for display.
type Change struct {
	What     string
	From, To string
}

func (c Change) String() string {
	switch {
	case c.From == "":
		return fmt.Sprintf("%s: + %s", c.What, c.To)
	case c.To == "":
		return fmt.Sprintf("%s: - %s", c.What, c.From)
	default:
		return fmt.Sprintf("%s: %s -> %s", c.What, c.From, c.To)
	}
}

// SSHAgent says whether and how to manage the forwarded ssh-agent. It depends on
// the caller's session, so only interactive commands set Manage.
type SSHAgent struct {
	Manage   bool
	Socket   string
	UID, GID int
}

func instanceType(m *manifest.Machine) string {
	if m.IsVM() {
		return string(api.InstanceTypeVM)
	}
	return string(api.InstanceTypeContainer)
}

// Immutable lists the differences that Incus can't apply in place; fixing them
// means recreating the instance (and losing its root disk).
func Immutable(m *manifest.Machine, inst *api.Instance) []Change {
	var out []Change
	if want := instanceType(m); inst.Type != want {
		out = append(out, Change{"type", inst.Type, want})
	}
	// Recorded at creation; instances made by older versions don't have them.
	if os := inst.Config[KeyOS]; os != "" && os != m.OS {
		out = append(out, Change{"os", os, m.OS})
	}
	if img := inst.Config[KeyImage]; img != "" && img != m.Image {
		out = append(out, Change{"image", img, m.Image})
	}
	hasNIC := inst.Devices["eth0"]["type"] != "none"
	if wantNIC := m.Network.Policy != "none"; hasNIC != wantNIC {
		out = append(out, Change{"network", nicState(hasNIC), nicState(wantNIC)})
	}
	return out
}

func nicState(nic bool) string {
	if nic {
		return "interface"
	}
	return "none"
}

// CreateRequest builds the instance for a fresh machine. Root disk and NIC are
// copied from the default profile with our overrides, like `incus init -d`.
func CreateRequest(name string, m *manifest.Machine, profile *api.Profile, cloudInit, cloudNetwork string) (api.InstancesPost, error) {
	cfg := map[string]string{
		"limits.cpu":    strconv.Itoa(m.CPU),
		"limits.memory": m.Memory,
		KeyManaged:      "true",
		KeyOS:           m.OS,
		KeyImage:        m.Image,
	}
	if m.IsVM() {
		cfg["security.secureboot"] = "false"
	} else {
		cfg["security.syscalls.intercept.mknod"] = "true"
		cfg["security.syscalls.intercept.setxattr"] = "true"
		if m.Nesting {
			cfg["security.nesting"] = "true"
		}
		// host uid/gid 1000 == guest 1000, so shared dirs have sane ownership
		if len(m.Shares) > 0 {
			cfg["raw.idmap"] = "both 1000 1000"
		}
	}
	if m.Autostart {
		cfg["boot.autostart"] = "true"
	}
	if cloudInit != "" {
		cfg["cloud-init.user-data"] = cloudInit
	}
	if cloudNetwork != "" {
		cfg["cloud-init.network-config"] = cloudNetwork
	}

	root, ok := profile.Devices["root"]
	if !ok {
		return api.InstancesPost{}, fmt.Errorf("profile %s has no root device", profile.Name)
	}
	devices := map[string]map[string]string{"root": with(root, "size", m.Disk)}
	if m.Network.Policy == "none" {
		devices["eth0"] = map[string]string{"type": "none"}
	} else {
		eth0, ok := profile.Devices["eth0"]
		if !ok {
			return api.InstancesPost{}, fmt.Errorf("profile %s has no eth0 device", profile.Name)
		}
		devices["eth0"] = withFiltering(with(eth0, "network", m.Network.Bridge))
	}
	// Attached from creation: the guest never runs a moment without its policy.
	if m.Network.ACL != nil {
		devices = WithACL(devices, name)
	}

	return api.InstancesPost{
		Name: name,
		Type: api.InstanceType(instanceType(m)),
		InstancePut: api.InstancePut{
			Config:   cfg,
			Devices:  devices,
			Profiles: []string{profile.Name},
		},
	}, nil
}

func with(dev map[string]string, k, v string) map[string]string {
	out := maps.Clone(dev)
	out[k] = v
	return out
}

// Plan returns cur changed to match the settings that may drift from the spec:
// limits, root size, autostart, nesting, shares, the ssh-agent proxy and the
// network ACL attachment. Warnings are things it chose not to do or that need a restart.
func Plan(name string, m *manifest.Machine, cur api.InstancePut, agent SSHAgent) (api.InstancePut, []Change, []string) {
	want := clonePut(cur)
	var warnings []string

	want.Config["limits.cpu"] = strconv.Itoa(m.CPU)
	want.Config["limits.memory"] = m.Memory
	if m.Autostart {
		want.Config["boot.autostart"] = "true"
	} else {
		delete(want.Config, "boot.autostart")
	}
	if !m.IsVM() {
		if m.Nesting {
			want.Config["security.nesting"] = "true"
		} else {
			delete(want.Config, "security.nesting")
		}
		if want.Config["security.nesting"] != cur.Config["security.nesting"] {
			warnings = append(warnings, "security.nesting changes when the machine next restarts")
		}
	}
	// Adopt instances created before these keys existed.
	want.Config[KeyManaged] = "true"
	if want.Config[KeyOS] == "" {
		want.Config[KeyOS] = m.OS
	}
	if want.Config[KeyImage] == "" {
		want.Config[KeyImage] = m.Image
	}

	if root, ok := want.Devices["root"]; ok && root["size"] != m.Disk {
		if shrinks(root["size"], m.Disk) {
			warnings = append(warnings, fmt.Sprintf("not shrinking the root disk from %s to %s (unsupported)", root["size"], m.Disk))
		} else {
			root["size"] = m.Disk
		}
	}

	wanted := map[string]bool{}
	for _, s := range m.Shares {
		dev := sharePrefix + s.Name
		wanted[dev] = true
		want.Devices[dev] = map[string]string{
			"type": "disk", "source": s.Host, "path": s.Guest, "readonly": strconv.FormatBool(s.ReadOnly),
		}
		if equalShare(cur.Devices[dev], want.Devices[dev]) {
			want.Devices[dev] = cur.Devices[dev]
		}
	}
	for dev := range cur.Devices {
		if strings.HasPrefix(dev, sharePrefix) && !wanted[dev] {
			delete(want.Devices, dev)
		}
	}

	switch {
	case !m.SSHAgent:
		// Never leave a forwarded agent behind once the spec stops asking for it.
		delete(want.Devices, sshAgentDev)
	case !agent.Manage:
	case agent.Socket == "":
		warnings = append(warnings, "sshAgent wanted but no SSH_AUTH_SOCK in this shell; skipping")
	default:
		want.Devices[sshAgentDev] = map[string]string{
			"type": "proxy", "listen": "unix:/run/ssh-agent.sock", "connect": "unix:" + agent.Socket,
			"bind": "instance", "uid": "1000", "gid": "1000", "mode": "0600",
			"security.uid": strconv.Itoa(agent.UID), "security.gid": strconv.Itoa(agent.GID),
		}
	}

	if m.Network.ACL != nil {
		want.Devices = WithACL(want.Devices, name)
	} else {
		want.Devices = WithoutACL(want.Devices)
	}
	if eth0, ok := want.Devices["eth0"]; ok && eth0["type"] != "none" {
		eth0 = withFiltering(eth0)
		// Restricted and open machines use different bridges; moving re-plugs the NIC (new address).
		if eth0["network"] != m.Network.Bridge {
			eth0["network"] = m.Network.Bridge
			warnings = append(warnings, fmt.Sprintf("eth0 moves to bridge %s: the interface is re-plugged and gets a new address", m.Network.Bridge))
		}
		want.Devices["eth0"] = eth0
	}

	return want, Diff(cur, want), warnings
}

// withFiltering stops a guest from spoofing its MAC or IP on the shared bridge
// (ARP poisoning, impersonating other guests or the gateway). Incus pins the
// guest to its DHCP-allocated IPv4 address; the bridge has no IPv6, so all IPv6
// is dropped. Every policy gets it, open included: an open guest could otherwise
// intercept a restricted one's traffic. These keys apply live, without re-plugging the NIC.
func withFiltering(eth0 map[string]string) map[string]string {
	out := maps.Clone(eth0)
	out["security.mac_filtering"] = "true"
	out["security.ipv4_filtering"] = "true"
	out["security.ipv6_filtering"] = "true"
	return out
}

// ACLAttached reports whether eth0 carries the machine's ACL with default drop.
func ACLAttached(devices map[string]map[string]string, name string) bool {
	eth0 := devices["eth0"]
	return eth0["security.acls"] == ACLName(name) &&
		eth0["security.acls.default.egress.action"] == "drop" &&
		eth0["security.acls.default.ingress.action"] == "drop"
}

// Forbidden lists settings sacabox never sets and refuses to work around: they
// bypass the isolation the spec describes (privileged containers, raw overrides,
// any idmap other than the one used for shares).
func Forbidden(inst *api.Instance) []string {
	cfg := inst.ExpandedConfig
	if cfg == nil {
		cfg = inst.Config
	}
	var out []string
	if b, _ := strconv.ParseBool(cfg["security.privileged"]); b {
		out = append(out, "security.privileged=true")
	}
	for _, k := range []string{"raw.lxc", "raw.qemu", "raw.qemu.conf", "raw.apparmor", "raw.seccomp"} {
		if cfg[k] != "" {
			out = append(out, k)
		}
	}
	if v := cfg["raw.idmap"]; v != "" && v != "both 1000 1000" {
		out = append(out, "raw.idmap="+v)
	}
	sort.Strings(out)
	return out
}

func shrinks(from, to string) bool {
	f, err1 := units.ParseByteSizeString(from)
	t, err2 := units.ParseByteSizeString(to)
	return err1 == nil && err2 == nil && t < f
}

func equalShare(cur, want map[string]string) bool {
	if cur == nil {
		return false
	}
	ro := func(d map[string]string) bool { b, _ := strconv.ParseBool(d["readonly"]); return b }
	return cur["type"] == "disk" && cur["source"] == want["source"] && cur["path"] == want["path"] && ro(cur) == ro(want)
}

// Diff lists config keys and devices that differ between two instance configs.
func Diff(a, b api.InstancePut) []Change {
	var out []Change
	for _, k := range unionKeys(a.Config, b.Config) {
		if a.Config[k] != b.Config[k] {
			out = append(out, Change{"config " + k, a.Config[k], b.Config[k]})
		}
	}
	for _, k := range unionKeys(a.Devices, b.Devices) {
		if !maps.Equal(a.Devices[k], b.Devices[k]) {
			out = append(out, Change{"device " + k, deviceString(a.Devices[k]), deviceString(b.Devices[k])})
		}
	}
	return out
}

func deviceString(d map[string]string) string {
	if d == nil {
		return ""
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + d[k]
	}
	return strings.Join(parts, ",")
}

func unionKeys[V any](a, b map[string]V) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// WantACL is the ACL a machine's policy needs, or nil for open/none.
func WantACL(m *manifest.Machine) *api.NetworkACLPut {
	if m.Network.ACL == nil {
		return nil
	}
	return &api.NetworkACLPut{
		Description: "managed by sacabox",
		Egress:      orEmpty(m.Network.ACL.Egress),
		Ingress:     orEmpty(m.Network.ACL.Ingress),
	}
}

// ACLDiffers reports whether the ACL in Incus has different rules than want.
func ACLDiffers(cur *api.NetworkACL, want *api.NetworkACLPut) bool {
	return cur.Description != want.Description ||
		!reflect.DeepEqual(orEmpty(cur.Egress), want.Egress) ||
		!reflect.DeepEqual(orEmpty(cur.Ingress), want.Ingress)
}

func orEmpty(r []api.NetworkACLRule) []api.NetworkACLRule {
	if r == nil {
		return []api.NetworkACLRule{}
	}
	return r
}

// WithACL returns devices with the machine's ACL attached to eth0: everything
// not explicitly allowed is dropped.
func WithACL(devices map[string]map[string]string, machine string) map[string]map[string]string {
	eth0, ok := devices["eth0"]
	if !ok || eth0["type"] == "none" {
		return devices
	}
	out := maps.Clone(devices)
	eth0 = maps.Clone(eth0)
	eth0["security.acls"] = ACLName(machine)
	eth0["security.acls.default.egress.action"] = "drop"
	eth0["security.acls.default.ingress.action"] = "drop"
	out["eth0"] = eth0
	return out
}

// WithoutACL drops every trace of an ACL from eth0, for a machine whose policy
// has none (open).
func WithoutACL(devices map[string]map[string]string) map[string]map[string]string {
	eth0, ok := devices["eth0"]
	if !ok {
		return devices
	}
	out := maps.Clone(devices)
	eth0 = maps.Clone(eth0)
	delete(eth0, "security.acls")
	delete(eth0, "security.acls.default.egress.action")
	delete(eth0, "security.acls.default.ingress.action")
	out["eth0"] = eth0
	return out
}
