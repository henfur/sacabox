# AGENTS.md

Guidance for coding agents working on sacabox: a NixOS module plus a `vm` CLI that manage Incus
VMs/containers declared in Nix. Read `README.md` first for the user-facing picture. This file
covers how the code fits together and what to avoid.

## Scope

This repo is the public module only. User machine definitions live in a separate, private
directory/repo and are imported next to the module; they are out of scope. Only `examples/` belongs
here, and it must stay generic.

- Never add personal data: real names, emails, hostnames, home paths, ssh keys, internal IPs, repo
  URLs. Use `you`, `/home/you/...`, and documentation ranges (`10.20.0.0/24`, `203.0.113.0/24`).
- Don't add machine configs that are specific to one person's setup, even as examples.

## Architecture

```
default.nix     entry: assertions, Incus preseed, writes /etc/sacabox/machines.json, installs `vm`
options.nix     host options under services.sacabox.*
machine.nix     per-machine submodule (also the schema for templates) + read-only `spec`
templates.nix   built-in templates dev / agent / ops
guest-nixos.nix generates configuration.nix and workstation-guest.nix pushed into NixOS guests
cli.nix         the `vm` script (writeShellApplication, so shellcheck runs at build time)
lib/cidr.nix    carves ranges out of a CIDR (used for the `internet` policy)
ci/             check.sh runs all CI checks locally (.github/workflows/ci.yml calls it)
apply.sh        update flake inputs, rebuild the host, then run `vm` for one machine
examples/       sample machines directory, one subdirectory per machine
```

Data flow: Nix options -> `machines.<name>.spec` -> JSON manifest at `/etc/sacabox/machines.json`
-> `vm` reads it with `jq` and reconciles Incus. **All declarative state goes through the
manifest.** The CLI must not re-derive anything the Nix side can compute, and the Nix side must not
call Incus.

## Security

Security is the main goal of this project: isolation between the host and the guests, and between
guests, is the reason it exists. Changes must never weaken the security posture without explicit
validation from the maintainer. If a change could do any of the following, stop and ask first
instead of making it:

- widen a network policy, or change a default so a machine can reach more than before (LAN, host,
  other guests, private ranges);
- expose more of the host to a guest: shares, devices, forwarded sockets such as the ssh-agent,
  nesting, privileged containers, or looser `allowedShareRoots` or idmap settings;
- default something to on or less restricted, or grant more privilege to the guest user (sudo,
  groups, capabilities);
- let secrets or keys enter the Nix store, the manifest, logs or a guest;
- make provisioning fetch or run code from a new source without pinning or verification.

When in doubt, keep the stricter behaviour as the default and make loosening an explicit,
per-machine opt-in. Say plainly in your summary when a change touches any of these areas.

## Conventions and pitfalls

- **Templates are option defaults.** In `machine.nix`, options default to `d [ "path" ] fallback`,
  which reads the chosen template's attrs. Do not make options depend on `config.template` through
  `mkIf` or `config`: it causes infinite recursion. Add new machine options in the same style and
  expose them in `spec`.
- **Network policies are pure allow-lists** (`open`, `internet`, `allowlist`, `none`). Don't rely on
  Incus's drop/allow rule ordering. The ACL is detached while provisioning and reattached after.
- **`vm` is a Nix string containing shell.** Escape `${` as `''${`, keep heredoc delimiters unique,
  and write escapes such as `\e` and `\a` as `\\e`, `\\a` in the Nix source. The build runs
  shellcheck: fix warnings or add a narrow `# shellcheck disable=...` with a reason.
- **`incus exec` has a minimal PATH.** Commands run in guests need the `gpath` constant (or
  absolute paths). NixOS guests keep tools under `/run/current-system/sw/bin`.
- **User and uid:** the guest user is always uid 1000. Host-side code relies on it for
  `--user 1000`, `raw.idmap` and the shell lookup.
- **Internal names use the `ws-` prefix** (ACLs `ws-<name>`, devices `ws-share-*`). Keep them:
  renaming orphans resources on existing machines.
- **The guest module is generated text** (`guest-nixos.nix`). `@NAME@` is replaced by the CLI at
  provision time. Anything added there must work on both VM and container images.
- **Home Manager runs on the host.** The activation closure is built on the host and shipped with
  `nix-store --export | incus exec ... nix-store --import`, then activated as uid 1000. Don't add
  steps that need network access inside the guest: the network policy may be locked down.
- **Nothing from the host leaks into a guest by default.** Host paths only via `shares` (checked
  against `allowedShareRoots`); the host ssh-agent only with `sshAgent = true`. Keep both opt-in.

## Verifying changes

There is no test suite; verify by evaluating and building.

```sh
# the shell script (runs shellcheck)
nix-build --no-out-link -E 'let pkgs = import <nixpkgs> {}; in
  import ./cli.nix { inherit (pkgs) lib pkgs; cfg = { uiHost = "x"; }; incus = pkgs.incus; }'

# the module against the examples (no host config needed)
nix-instantiate --eval --strict -E 'let pkgs = import <nixpkgs> {};
  m = pkgs.lib.evalModules { modules = [ ./options.nix ./templates.nix ./examples
    { _module.args = { inherit pkgs; inputs.dotfiles.homeModules.terminal = []; }; } ]; };
  in builtins.attrNames m.config.services.sacabox.machines'
```

`./ci/check.sh` runs the same checks as CI (syntax, statix/deadnix lint, example eval, shellcheck,
gitleaks); run it before finishing. Also evaluate the manifest in a real host configuration when you change `default.nix`,
`machine.nix` or `options.nix`. Do not run `nixos-rebuild`, `apply.sh` or `vm` against the user's
machine unless asked: they change the running system and create or modify VMs.

## Style

- Match the surrounding Nix: 2-space indent, short comments that explain *why*, no banner comments.
- Options need a `description`; give `example` when the value isn't obvious.
- Keep `README.md` and `examples/` in sync with any change to options, commands or layout.
- Keep changes small and focused, and don't reformat unrelated code.
