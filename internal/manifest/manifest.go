// Package manifest reads /etc/sacabox/machines.json: the fully resolved machine
// descriptions written by the NixOS module. Nothing here re-derives what Nix computed.
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/lxc/incus/v7/shared/api"
)

// Version is the manifest schema this binary understands (default.nix writes it).
const Version = 1

const DefaultPath = "/etc/sacabox/machines.json"

type Manifest struct {
	Version int    `json:"version"`
	User    string `json:"user"`
	Home    string `json:"home"`
	Flake   Flake  `json:"flake"`
	// AllowedShareRoots is re-checked at runtime, against the paths as they are then.
	AllowedShareRoots []string            `json:"allowedShareRoots"`
	Terminal          Terminal            `json:"terminal"`
	Machines          map[string]*Machine `json:"machines"`
	Templates         map[string]*Machine `json:"templates"`
}

type Flake struct {
	Path   string   `json:"path"`
	Host   string   `json:"host"`
	Inputs []string `json:"inputs"`
}

type Terminal struct {
	Enable    bool   `json:"enable"`
	Alacritty string `json:"alacritty"`
}

// Machine is one entry of `machines` (or `templates`): the `spec` option of machine.nix.
// Store paths that Nix sets to null decode as "".
type Machine struct {
	OS               string  `json:"os"`
	Type             string  `json:"type"`
	Autostart        bool    `json:"autostart"`
	Snapshot         bool    `json:"snapshot"`
	Nesting          bool    `json:"nesting"`
	SSHAgent         bool    `json:"sshAgent"`
	RecreateOnChange bool    `json:"recreateOnChange"`
	Color            string  `json:"color"`
	Image            string  `json:"image"`
	CPU              int     `json:"cpu"`
	Memory           string  `json:"memory"`
	Disk             string  `json:"disk"`
	Shares           []Share `json:"shares"`
	Network          Network `json:"network"`
	CloudInit        string  `json:"cloudInit"`
	CloudNetwork     string  `json:"cloudNetwork"`
	NixOS            *NixOS  `json:"nixos"`
	Terminal         struct {
		Settings map[string]any `json:"settings"`
	} `json:"terminal"`
	Init     string `json:"init"`
	Dotfiles string `json:"dotfiles"`
	Home     string `json:"home"`

	// Hash identifies this exact spec; stored on the instance after a successful provision.
	Hash string `json:"-"`
}

type Share struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Guest    string `json:"guest"`
	ReadOnly bool   `json:"readOnly"`
}

type Network struct {
	Policy      string   `json:"policy"`
	Bridge      string   `json:"bridge"`
	ACL         *ACL     `json:"acl"`
	Nameservers []string `json:"nameservers"`
}

// ACL rules are generated in Incus's own shape by machine.nix.
type ACL struct {
	Egress  []api.NetworkACLRule `json:"egress"`
	Ingress []api.NetworkACLRule `json:"ingress"`
}

type NixOS struct {
	Channel       string        `json:"channel"`
	Nameservers   []string      `json:"nameservers"`
	Modules       []NixOSModule `json:"modules"`
	Configuration string        `json:"configuration"`
	GuestModule   string        `json:"guestModule"`
}

type NixOSModule struct {
	Idx   int    `json:"idx"`
	Src   string `json:"src"`
	IsDir bool   `json:"isDir"`
}

func (m *Machine) IsVM() bool { return m.Type == "vm" }

// Load reads and checks the manifest at path.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

func Parse(data []byte) (*Manifest, error) {
	var raw struct {
		Version  int                        `json:"version"`
		Machines map[string]json.RawMessage `json:"machines"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if raw.Version != Version {
		return nil, fmt.Errorf("manifest: schema version %d, this sacabox understands %d (rebuild the host so both match)", raw.Version, Version)
	}
	// Strict: a misspelt key (say in an ACL rule) must fail, not silently drop a restriction.
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	for name, spec := range raw.Machines {
		m.Machines[name].Hash = hash(spec)
	}
	return &m, nil
}

func hash(spec json.RawMessage) string {
	var buf bytes.Buffer
	// Nix writes the JSON with sorted keys, so compacting is enough to make it canonical.
	_ = json.Compact(&buf, spec)
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

// Machine returns the declared machine called name.
func (m *Manifest) Machine(name string) (*Machine, error) {
	if spec, ok := m.Machines[name]; ok {
		return spec, nil
	}
	return nil, fmt.Errorf("no machine '%s' declared (add <name>/default.nix to your machines directory; declared: %s)",
		name, joinKeys(m.Machines))
}

func (m *Manifest) MachineNames() []string { return sortedKeys(m.Machines) }

func (m *Manifest) DeclaredSummary() string {
	return fmt.Sprintf("declared: %s  |  templates: %s", joinKeys(m.Machines), joinKeys(m.Templates))
}

func sortedKeys(ms map[string]*Machine) []string {
	keys := make([]string, 0, len(ms))
	for k := range ms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func joinKeys(ms map[string]*Machine) string {
	var b bytes.Buffer
	for i, k := range sortedKeys(ms) {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k)
	}
	return b.String()
}
