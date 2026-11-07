package reconcile

import (
	"fmt"
	"maps"
	"net/http"
	"strings"

	"github.com/lxc/incus/v7/shared/api"

	"sacabox/internal/incus"
)

// fake is an in-memory Incus with just enough behaviour for Provision.
type fake struct {
	instances map[string]*api.Instance
	acls      map[string]*api.NetworkACL
	snapshots []string
	saved     map[string]api.InstancePut
	execs     []string
	// failExec makes commands starting with this string exit 1.
	failExec string
	// aclDuringExec records whether eth0 had the ACL attached when each command ran.
	aclDuringExec map[string]string
}

var _ incus.Server = (*fake)(nil)

func newFake() *fake {
	return &fake{instances: map[string]*api.Instance{}, acls: map[string]*api.NetworkACL{}, aclDuringExec: map[string]string{},
		saved: map[string]api.InstancePut{}}
}

func notFound() error { return api.StatusErrorf(http.StatusNotFound, "not found") }

func clone(put api.InstancePut) api.InstancePut {
	out := put
	out.Config = maps.Clone(put.Config)
	out.Devices = map[string]map[string]string{}
	for k, d := range put.Devices {
		out.Devices[k] = maps.Clone(d)
	}
	return out
}

func (f *fake) GetInstance(name string) (*api.Instance, string, error) {
	inst, ok := f.instances[name]
	if !ok {
		return nil, "", notFound()
	}
	out := *inst
	out.InstancePut = clone(inst.InstancePut)
	return &out, "", nil
}

// checkACLs mimics Incus refusing a NIC that references an ACL that doesn't exist.
func (f *fake) checkACLs(put api.InstancePut) error {
	if acl := put.Devices["eth0"]["security.acls"]; acl != "" {
		if _, ok := f.acls[acl]; !ok {
			return fmt.Errorf("network ACL %q not found", acl)
		}
	}
	return nil
}

func (f *fake) Create(_ string, req api.InstancesPost) error {
	if err := f.checkACLs(req.InstancePut); err != nil {
		return err
	}
	f.instances[req.Name] = &api.Instance{
		Name: req.Name, Type: string(req.Type), InstancePut: clone(req.InstancePut), StatusCode: api.Stopped,
	}
	return nil
}

func (f *fake) UpdateInstance(name string, put api.InstancePut, _ string) error {
	inst, ok := f.instances[name]
	if !ok {
		return notFound()
	}
	if put.Restore != "" {
		saved, ok := f.saved[name+"/"+put.Restore]
		if !ok {
			return notFound()
		}
		inst.InstancePut = clone(saved)
		return nil
	}
	if err := f.checkACLs(put); err != nil {
		return err
	}
	inst.InstancePut = clone(put)
	return nil
}

func (f *fake) Delete(name string) error { delete(f.instances, name); return nil }

func (f *fake) SetState(name, action string, _ bool) error {
	inst, ok := f.instances[name]
	if !ok {
		return notFound()
	}
	if action == "start" {
		inst.StatusCode = api.Running
	} else {
		inst.StatusCode = api.Stopped
	}
	return nil
}

func (f *fake) GetProfile(name string) (*api.Profile, error) {
	return &api.Profile{Name: name, ProfilePut: api.ProfilePut{Devices: map[string]map[string]string{
		"root": {"type": "disk", "path": "/", "pool": "default"},
		"eth0": {"type": "nic", "name": "eth0", "network": "incusbr0"},
	}}}, nil
}

func (f *fake) Exec(name string, o incus.ExecOpts) (int, error) {
	cmd := strings.Join(o.Cmd, " ")
	f.execs = append(f.execs, cmd)
	f.aclDuringExec[o.Cmd[0]] = f.instances[name].Devices["eth0"]["security.acls"]
	if f.failExec != "" && strings.HasPrefix(cmd, f.failExec) {
		return 1, nil
	}
	return 0, nil
}

func (f *fake) PushFile(string, string, []byte, int64, int64, int) error { return nil }

func (f *fake) CreateSnapshot(name, snapshot string) error {
	f.saved[name+"/"+snapshot] = clone(f.instances[name].InstancePut)
	f.snapshots = append(f.snapshots, fmt.Sprintf("%s/%s acl=%s", name, snapshot, f.instances[name].Devices["eth0"]["security.acls"]))
	return nil
}

func (f *fake) GetNetworkACL(name string) (*api.NetworkACL, string, error) {
	acl, ok := f.acls[name]
	if !ok {
		return nil, "", notFound()
	}
	return acl, "", nil
}

func (f *fake) CreateNetworkACL(acl api.NetworkACLsPost) error {
	f.acls[acl.Name] = &api.NetworkACL{NetworkACLPost: acl.NetworkACLPost, NetworkACLPut: acl.NetworkACLPut}
	return nil
}

func (f *fake) UpdateNetworkACL(name string, put api.NetworkACLPut, _ string) error {
	f.acls[name].NetworkACLPut = put
	return nil
}

func (f *fake) DeleteNetworkACL(name string) error {
	if _, ok := f.acls[name]; !ok {
		return notFound()
	}
	delete(f.acls, name)
	return nil
}
