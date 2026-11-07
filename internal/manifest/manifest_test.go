package manifest

import (
	"strings"
	"testing"
)

const doc = `{"version":1,"user":"you","home":"/home/you",
  "flake":{"path":null,"host":"h","inputs":[]},
  "terminal":{"enable":false,"alacritty":null},
  "machines":{"dev":{"cpu":2,"image":"images:debian/13/cloud","init":null,"network":{"acl":null,"bridge":"incusbr0","policy":"open"},"os":"generic","type":"vm"}},
  "templates":{}}`

func TestParse(t *testing.T) {
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	dev, err := m.Machine("dev")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Init != "" || m.Flake.Path != "" || dev.Network.ACL != nil || len(dev.Hash) != 64 {
		t.Errorf("parsed %+v", dev)
	}
	if _, err := m.Machine("nope"); err == nil || !strings.Contains(err.Error(), "declared: dev") {
		t.Errorf("unknown machine: %v", err)
	}
}

func TestHashTracksSpec(t *testing.T) {
	a, _ := Parse([]byte(doc))
	// same document, different formatting: same hash
	b, _ := Parse([]byte(strings.ReplaceAll(doc, `"cpu":2,`, "\n  \"cpu\": 2,")))
	c, _ := Parse([]byte(strings.Replace(doc, `"cpu":2`, `"cpu":4`, 1)))
	if a.Machines["dev"].Hash != b.Machines["dev"].Hash {
		t.Error("hash depends on formatting")
	}
	if a.Machines["dev"].Hash == c.Machines["dev"].Hash {
		t.Error("hash ignores a spec change")
	}
}

func TestVersionMismatch(t *testing.T) {
	if _, err := Parse([]byte(strings.Replace(doc, `"version":1`, `"version":2`, 1))); err == nil {
		t.Error("unknown manifest version accepted")
	}
	if _, err := Parse([]byte(`{"machines":{}}`)); err == nil {
		t.Error("manifest without version accepted")
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	// a misspelt ACL key must not silently drop the restriction
	bad := strings.Replace(doc, `"acl":null`, `"acl":{"egress":[{"action":"allow","destination":"203.0.113.0/24","destination_prot":"22","state":"enabled"}],"ingress":[]}`, 1)
	if _, err := Parse([]byte(bad)); err == nil {
		t.Error("unknown ACL rule field accepted")
	}
}
