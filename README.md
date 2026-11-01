# sacabox

A NixOS module that sets up [Incus](https://linuxcontainers.org/incus/) for fast, isolated
development and operations environments. VMs and containers are declared in Nix, created on first
use with `vm sh <name>`, and each one gets its own network and host-directory restrictions.

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
- **A small CLI**, `vm`: `sh`, `up`, `provision`, `home`, `root`, `ls`, `ip`, `snap`, `reset`,
  `stop`, `start`, `rm`, `tmp`.

## Layout

```
default.nix          entry point (import this): manifest, Incus preseed
options.nix          host options
machine.nix          per-machine options
templates.nix        built-in templates
guest-nixos.nix      files generated for NixOS guests
cli.nix              the `vm` command
lib/                 helpers
examples/            sample machines directory (copy it to your own repo)
apply.sh             update flake inputs, rebuild the host, then provision a machine
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
vm sh dev          # create, start and enter
vm ls              # running and declared machines
./apply.sh dev     # after editing the machine's config
```

`apply.sh MACHINE [provision|home|up]` refreshes the flake inputs and rebuilds the host. It then
runs `vm provision`, `vm home` or `vm up` for that machine.

## Status

Personal project, used day to day on NixOS. Expect rough edges.

## AI usage disclosure

Large language model tools (Claude, via Claude Code) were used to help develop this project,
including writing and refactoring code and documentation. I direct the design and review and test
the result, but parts of the code were generated, so read it before running it on a machine you
care about.
