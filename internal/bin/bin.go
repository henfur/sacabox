// Package bin holds the absolute paths of the external tools sacabox runs.
// The Nix build sets them with -ldflags; a plain `go build` falls back to $PATH.
package bin

var (
	Incus    = "incus"
	Nix      = "nix"
	NixStore = "nix-store"
)

// GuestPath is the PATH given to commands run in guests: `incus exec` starts
// with a minimal one, and NixOS guests keep their tools elsewhere.
const GuestPath = "/run/wrappers/bin:/run/current-system/sw/bin:/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin"
