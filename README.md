# sacabox

> **WIP:** This project is subject to major structural changes, it may break on any commit while I find a stable structure.

![sacabox-logo](./assets/logo.png)

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
- **A small CLI**, `sacabox`: `sh`, `term`, `provision`, `root`, `ls`, `reset`, `stop`, `rm`, `tmp`.

## Layout

```
default.nix          entry point (import this): manifest, Incus preseed
options.nix          host options
machine.nix          per-machine options
templates.nix        built-in templates
guest-nixos.nix      files generated for NixOS guests
cli.nix              the `sacabox` command
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
