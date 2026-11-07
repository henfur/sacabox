// Package incus is the small slice of the Incus API sacabox uses, behind an
// interface so the reconcile logic can run against a fake in tests.
package incus

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/cliconfig"

	"sacabox/internal/bin"
)

type Server interface {
	GetInstance(name string) (*api.Instance, string, error)
	// Create makes an instance from an image reference as the CLI takes it ("images:debian/13/cloud").
	Create(image string, req api.InstancesPost) error
	UpdateInstance(name string, put api.InstancePut, etag string) error
	Delete(name string) error
	SetState(name, action string, force bool) error
	GetProfile(name string) (*api.Profile, error)
	Exec(name string, opts ExecOpts) (int, error)
	PushFile(name, path string, content []byte, uid, gid int64, mode int) error
	CreateSnapshot(name, snapshot string) error

	GetNetworkACL(name string) (*api.NetworkACL, string, error)
	CreateNetworkACL(acl api.NetworkACLsPost) error
	UpdateNetworkACL(name string, put api.NetworkACLPut, etag string) error
	DeleteNetworkACL(name string) error
}

type ExecOpts struct {
	Cmd    []string
	Env    map[string]string
	User   uint32
	Group  uint32
	Cwd    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// IsNotFound reports whether err is an Incus 404.
func IsNotFound(err error) bool { return api.StatusErrorCheck(err, http.StatusNotFound) }

type client struct{ d incus.InstanceServer }

// Connect opens the local Incus daemon over its unix socket.
func Connect() (Server, error) {
	d, err := incus.ConnectIncusUnix("", nil)
	if err != nil {
		return nil, fmt.Errorf("connect to incus: %w", err)
	}
	return &client{d: d}, nil
}

func wait(op incus.Operation, err error) error {
	if err != nil {
		return err
	}
	return op.Wait()
}

func (c *client) GetInstance(name string) (*api.Instance, string, error) {
	return c.d.GetInstance(name)
}

func (c *client) Create(image string, req api.InstancesPost) error {
	// The same remotes as the `incus` CLI ("images:", user-added ones). Without a
	// config dir (e.g. no $HOME under systemd) the built-in defaults still apply.
	conf, err := cliconfig.LoadConfig("")
	if err != nil {
		conf = cliconfig.DefaultConfig()
	}
	remote, ref, err := conf.ParseRemote(image)
	if err != nil {
		return err
	}

	var src incus.ImageServer = c.d
	if remote != "local" {
		if src, err = conf.GetImageServer(remote); err != nil {
			return err
		}
	}

	// Mirrors the CLI: public image servers are addressed by alias, Incus remotes are resolved first.
	info := &api.Image{Fingerprint: ref}
	info.Public = true
	if remote == "local" || conf.Remotes[remote].Protocol == "incus" {
		if alias, _, err := src.GetImageAlias(ref); err == nil {
			req.Source.Alias = ref
			ref = alias.Target
		}
		if info, _, err = src.GetImage(ref); err != nil {
			return fmt.Errorf("image %s: %w", image, err)
		}
	} else {
		req.Source.Alias = ref
	}

	op, err := c.d.CreateInstanceFromImage(src, *info, req)
	if err != nil {
		return err
	}
	return op.Wait()
}

func (c *client) UpdateInstance(name string, put api.InstancePut, etag string) error {
	return wait(c.d.UpdateInstance(name, put, etag))
}

func (c *client) Delete(name string) error { return wait(c.d.DeleteInstance(name)) }

func (c *client) SetState(name, action string, force bool) error {
	return wait(c.d.UpdateInstanceState(name, api.InstanceStatePut{Action: action, Timeout: -1, Force: force}, ""))
}

func (c *client) GetProfile(name string) (*api.Profile, error) {
	p, _, err := c.d.GetProfile(name)
	return p, err
}

// Exec runs a command in the guest and returns its exit status.
func (c *client) Exec(name string, o ExecOpts) (int, error) {
	env := map[string]string{"PATH": bin.GuestPath}
	for k, v := range o.Env {
		env[k] = v
	}
	stdin, stdout, stderr := o.Stdin, o.Stdout, o.Stderr
	if stdin == nil {
		stdin = bytes.NewReader(nil)
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	done := make(chan bool)
	op, err := c.d.ExecInstance(name, api.InstanceExecPost{
		Command: o.Cmd, WaitForWS: true, Environment: env, User: o.User, Group: o.Group, Cwd: o.Cwd,
	}, &incus.InstanceExecArgs{Stdin: stdin, Stdout: stdout, Stderr: stderr, DataDone: done})
	if err != nil {
		return -1, err
	}
	if err := op.Wait(); err != nil {
		return -1, err
	}
	<-done
	code, ok := op.Get().Metadata["return"].(float64)
	if !ok {
		return -1, errors.New("exec: no exit status returned")
	}
	return int(code), nil
}

func (c *client) PushFile(name, path string, content []byte, uid, gid int64, mode int) error {
	return c.d.CreateInstanceFile(name, path, incus.InstanceFileArgs{
		Content: bytes.NewReader(content), UID: uid, GID: gid, Mode: mode, Type: "file", WriteMode: "overwrite",
	})
}

func (c *client) CreateSnapshot(name, snapshot string) error {
	return wait(c.d.CreateInstanceSnapshot(name, api.InstanceSnapshotsPost{Name: snapshot}))
}

func (c *client) GetNetworkACL(name string) (*api.NetworkACL, string, error) {
	return c.d.GetNetworkACL(name)
}

func (c *client) CreateNetworkACL(acl api.NetworkACLsPost) error { return c.d.CreateNetworkACL(acl) }

func (c *client) UpdateNetworkACL(name string, put api.NetworkACLPut, etag string) error {
	return c.d.UpdateNetworkACL(name, put, etag)
}

func (c *client) DeleteNetworkACL(name string) error { return c.d.DeleteNetworkACL(name) }
