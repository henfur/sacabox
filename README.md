# sacabox

> **WIP:** This project is subject to major structural changes, it may break on any commit while I find a stable structure.

<p align="center">
  <img width=300 src="./assets/logo.png" />
</p>

A NixOS module that sets up [Incus](https://linuxcontainers.org/incus/) for fast, isolated
development and operations environments. VMs and containers are declared in Nix, created on first
use with `sacabox sh <name>`, and each one gets its own network and host-directory restrictions.

Typical uses: a dev box per project, a sandbox for AI coding agents, a locked-down machine that
holds the keys for production access.

## What you get

- **Declarative machines.** One directory per machine, built from templates (`dev`, `agent`, `ops`
  built in) with per-machine overrides for resources, shares, network and autostart.
- **NixOS guests** take ordinary NixOS modules (`nixos.modules`). Other distros take an init script
  or text, run through cloud-init and Incus.
- **Network policies** per machine as pure allow-lists: `open`, `internet` (public addresses only,
  no LAN or host), `allowlist`, or `none`, plus extra `allow` rules for CIDRs and ports.
- **Host access is opt-in.** The only host files a guest sees are its declared `shares`, limited to
  `allowedShareRoots`. The host ssh-agent is forwarded only when `sshAgent = true`.
- **Home Manager in the guest.** Modules from your own flake are built on the host and activated in
  the VM over the Incus agent, so they work even while the network is locked down.
- **Dedicated terminal windows** (optional). With `services.sacabox.terminal.enable`, `sacabox term NAME`
  opens an alacritty window with its own class and per-machine settings (`terminal.settings`,
  e.g. a distinct background colour) that drops you into the
  machine, and each machine gets a launcher entry ("VM: NAME") for rofi, the COSMIC launcher and
  other desktop launchers.
- **A small CLI**, `sacabox` (Go, talks to the Incus API): `sh`, `term`, `provision`, `reconcile`,
  `plan`, `root`, `ls`, `reset`, `stop`, `rm`, `tmp`.

## Layout

```
default.nix          entry point (import this): manifest, Incus preseed
options.nix          host options
machine.nix          per-machine options
templates.nix        built-in templates
guest-nixos.nix      files generated for NixOS guests
cli.nix              builds the `sacabox` command
cmd/, internal/      its Go source
lib/                 helpers
examples/            sample machines directory (copy it to your own repo)
```

Keep your real machines in a separate directory or repo and import it next to the module.
`examples/` shows the layout: `<name>/default.nix` declares a machine, with `guest.nix`, `ssh.nix`
and so on beside it.

## Usage

Import the module and your machines in your host configuration (a flake in this example, with both
as `flake = false` inputs):

```nix
modules = [ "${inputs.sacabox}" "${inputs.machines}" ];
```

Enable it:

```nix
services.sacabox = {
  enable = true;
  user = "you";
  sshKeys = [ "ssh-ed25519 AAAA..." ];
  allowedShareRoots = [ "/home/you/projects" ];
};
```

Declare a machine in `machines/dev/default.nix`:

```nix
{
  services.sacabox.machines.dev = {
    template = "dev";
    shares.projects.host = "/home/you/projects";
  };
}
```

Then:

```
sacabox sh dev          # create, provision and enter
sacabox ls              # running and declared machines
sacabox provision dev   # after editing the machine's config
```

`sacabox provision NAME` makes the machine match your config files. If `services.sacabox.flake`
is set, it first refreshes the listed flake inputs and rebuilds the host, so the new definitions
are in place. It then re-applies everything (resources, shares, network policy, guest system, init
script, dotfiles, Home Manager) and is safe to run repeatedly. `--no-rebuild` skips the host
rebuild.

`sacabox plan NAME` (or `--all`) shows what would change without touching anything, and
`sacabox reconcile NAME` (or `--all`) is `provision --no-rebuild` that skips machines already up
to date: each machine records the hash of the spec it was last provisioned with.

Provisioning runs under the machine's own network policy: it is attached from creation and never
lifted, so updating a running machine neither cuts off nor opens up its services. Anything
provisioning downloads must therefore be reachable under that policy. For now NixOS guests still
rebuild inside the guest from cache.nixos.org, which `internet` allows; under `allowlist` or
`none` add the cache to `network.allow` (this goes away once guests are built on the host).

`internet` and `allowlist` machines sit on their own bridge (`services.sacabox.network.restricted`,
`incusbr1` by default), separate from `open` ones. There the host only hands out addresses: it
answers no DNS and accepts nothing but DHCP. Such a machine resolves names through the resolvers
in its `network.nameservers`, which sacabox configures inside the guest and allows through its
policy on port 53:

```nix
services.sacabox.machines.agent.network = {
  policy = "internet";
  nameservers = [ "9.9.9.9" ];   # any resolver the guest may use
};
```

NixOS guests get them as `networking.nameservers`; other distros through cloud-init's network
config when the machine is created. Changing a machine's policy between `open` and a restricted
one moves its interface to the other bridge, which briefly re-plugs it and gives it a new address.

Every NIC also gets Incus's MAC and IP filtering: a guest can only use its own MAC and its
DHCP-assigned IPv4 address (no IPv6), so it can't impersonate another guest or the gateway on the
shared bridge.

The host's nftables ruleset is kept to the configuration (`services.sacabox.nftables.exclusive`,
on by default): on every switch, tables that aren't declared in `networking.nftables.tables`,
owned by Incus or listed in `services.sacabox.nftables.keep` are deleted, and the declared ones
are reloaded, so rules added by hand don't survive a rebuild. Declare your own rules as
`networking.nftables.tables`; a raw `networking.nftables.ruleset` (and `flushRuleset`, which would
also delete Incus's tables) is refused.

`sacabox reset` rolls back to the `clean` snapshot and re-applies the current config (policy,
shares, ssh-agent) before the machine boots. Machines with settings sacabox never sets
(`security.privileged`, `raw.lxc` and other `raw.*` keys, a foreign `raw.idmap`) are refused.
Share sources are re-checked when a machine is provisioned: they must still be real directories
under `allowedShareRoots`, with no symlink below the root.

Some settings can't change on an existing machine: `type`, `os`, `image`, and the network policy
to or from `none`. `sacabox` refuses those and leaves the machine alone, unless the machine sets
`recreateOnChange = true`, in which case it is deleted and recreated (losing its root disk). Only
instances sacabox created are ever deleted this way.

```nix
services.sacabox.flake = {
  path = "/etc/nixos";
  inputs = [ "sacabox" "machines" "dotfiles" ];   # refreshed before each rebuild
};
```

## Status

Work in progress personal project, used day to day on NixOS. Expect rough edges.

## License

MIT, see [LICENSE](LICENSE).

## AI usage disclosure

Large language model tools (Claude, via Claude Code) were used to help develop this project,
including writing and refactoring code and documentation. I direct the design and review and test
the result, but parts of the code were generated.
