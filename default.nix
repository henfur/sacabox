# sacabox: declarative Incus machines for dev / AI-agent / ops work.
#
#   imports = [ ./sacabox ];
#   services.sacabox = { enable = true; user = "you"; sshKeys = [ "ssh-ed25519 ..." ]; };
#
# Machines live outside this module (see ./examples): import a directory of modules that
# set `services.sacabox.machines.<name>`, then `sacabox sh <name>` (creates on first use).
{ config, lib, pkgs, ... }:

let
  cfg = config.services.sacabox;
  inherit (lib) mkIf;

  # Machines and templates, fully resolved, for the CLI.
  evalTemplate = tname: attrs:
    (lib.evalModules {
      modules = [ (import ./machine.nix { inherit lib pkgs; hostCfg = cfg; }) { config = attrs; } ];
      specialArgs = { name = "tmpl-${tname}"; inherit pkgs; };
    }).config.spec;

  manifest = {
    inherit (cfg) user;
    home = "/home/${cfg.user}";
    flake = { inherit (cfg.flake) path host inputs; };
    machines = lib.mapAttrs (_: m: m.spec) cfg.machines;
    templates = lib.mapAttrs evalTemplate cfg.templates;
  };

  cli = import ./cli.nix { inherit lib pkgs cfg; incus = config.virtualisation.incus.package; };

  underRoot = root: p: p == root || lib.hasPrefix (root + "/") p;
in
{
  imports = [ ./options.nix ./templates.nix ];

  config = mkIf cfg.enable {
    assertions =
      [{
        assertion = lib.all (n: builtins.match "[a-z][a-z0-9-]*" n != null) (lib.attrNames cfg.machines);
        message = "services.sacabox.machines: names must match [a-z][a-z0-9-]*";
      }]
      ++ lib.flatten (lib.mapAttrsToList
        (mname: m: lib.mapAttrsToList
          (sname: s: {
            assertion = cfg.allowedShareRoots == [ ] || lib.any (r: underRoot r s.host) cfg.allowedShareRoots;
            message = "services.sacabox.machines.${mname}.shares.${sname}: ${s.host} is outside allowedShareRoots (${toString cfg.allowedShareRoots})";
          })
          m.shares)
        cfg.machines)
      ++ lib.mapAttrsToList
        (mname: m: {
          assertion = !(m.init.script != null && m.init.text != "");
          message = "services.sacabox.machines.${mname}: set init.script or init.text, not both";
        })
        cfg.machines;

    virtualisation.incus = {
      enable = true;
      ui.enable = cfg.ui;

      preseed = {
        networks = [{
          name = cfg.network.name;
          type = "bridge";
          config = {
            "ipv4.address" = cfg.network.subnet;
            "ipv4.nat" = "true";
            "ipv6.address" = "none";
          };
        }];

        storage_pools = [{
          name = "default";
          driver = cfg.storage.driver;
          config = lib.optionalAttrs (cfg.storage.source != null) { source = cfg.storage.source; };
        }];

        # Deliberately bare: everything machine-specific is set per instance by `sacabox`.
        profiles = [{
          name = "default";
          description = "root disk + bridge";
          devices = {
            root = { type = "disk"; path = "/"; pool = "default"; };
            eth0 = { type = "nic"; name = "eth0"; network = cfg.network.name; };
          };
        }];
      };
    };

    environment.etc."sacabox/machines.json".text = builtins.toJSON manifest;
    environment.systemPackages = [ cli ] ++ lib.optionals cfg.terminal.enable (
      [ cfg.terminal.package ]
      ++ lib.mapAttrsToList
        (n: _: pkgs.makeDesktopItem {
          name = "sacabox-${n}";
          desktopName = "VM: ${n}";
          comment = "Open a terminal inside the ${n} machine";
          exec = "${cli}/bin/sacabox term ${n}";
          icon = "utilities-terminal";
          categories = [ "System" ];
          startupWMClass = "sacabox-${n}";
        })
        cfg.machines);

    users.users.${cfg.user}.extraGroups = [ "incus-admin" ];
    # Containers sharing host dirs map host uid/gid 1000 straight through (raw.idmap "both 1000 1000").
    users.users.root.subUidRanges = [{ startUid = 1000; count = 1; }];
    users.users.root.subGidRanges = [{ startGid = 1000; count = 1; }];

    networking.nftables.enable = lib.mkDefault true;
    networking.firewall.trustedInterfaces = [ cfg.network.name ];
    networking.networkmanager.unmanaged = [ "interface-name:${cfg.network.name}" "interface-name:tap*" ];

    boot.kernelModules = [ "vhost_vsock" "vsock" ];
  };
}
