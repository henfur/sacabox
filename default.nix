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
    # bump together with manifest.Version in internal/manifest when the shape changes
    version = 1;
    inherit (cfg) user;
    home = "/home/${cfg.user}";
    flake = { inherit (cfg.flake) path host inputs; };
    # re-checked by the CLI at provision time, when symlinks may have changed
    inherit (cfg) allowedShareRoots;
    terminal = {
      inherit (cfg.terminal) enable;
      alacritty = if cfg.terminal.enable then "${cfg.terminal.package}/bin/alacritty" else null;
    };
    machines = lib.mapAttrs (_: m: m.spec) cfg.machines;
    templates = templateSpecs;
  };

  templateSpecs = lib.mapAttrs evalTemplate cfg.templates;

  cli = import ./cli.nix { inherit lib pkgs; incus = config.virtualisation.incus.package; };

  underRoot = root: p: p == root || lib.hasPrefix (root + "/") p;

  nft = config.networking.nftables;
  # Tables that survive pruning: declared ones plus the user's keep-list. Incus's are matched by name.
  keptTables = lib.mapAttrsToList (_: t: "${t.family} ${t.name}") (lib.filterAttrs (_: t: t.enable) nft.tables)
    ++ cfg.nftables.keep;
  pruneTables = pkgs.writeShellApplication {
    name = "sacabox-nftables-prune";
    runtimeInputs = [ pkgs.nftables pkgs.jq pkgs.gnugrep ];
    text = ''
      keep=${lib.escapeShellArg (lib.concatStringsSep "\n" keptTables)}
      nft -j list tables \
        | jq -r '.nftables[] | select(.table) | "\(.table.family) \(.table.name)"' \
        | while read -r family name; do
            # Incus owns these: guests' network policies and NAT live there
            [ "$name" = incus ] && continue
            grep -qxF "$family $name" <<<"$keep" && continue
            echo "deleting nftables table $family $name (not in the configuration)"
            nft delete table "$family" "$name"
          done
    '';
  };
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
      # templates too: `sacabox tmp <template>` mounts their shares
      ++ lib.flatten (lib.mapAttrsToList
        (tname: t: map
          (s: {
            assertion = cfg.allowedShareRoots == [ ] || lib.any (r: underRoot r s.host) cfg.allowedShareRoots;
            message = "services.sacabox.templates.${tname}.shares.${s.name}: ${s.host} is outside allowedShareRoots (${toString cfg.allowedShareRoots})";
          })
          t.shares)
        templateSpecs)
      ++ [
        {
          assertion = cfg.network.name != cfg.network.restricted.name && cfg.network.subnet != cfg.network.restricted.subnet;
          message = "services.sacabox.network.restricted must be a different bridge with a different subnet than services.sacabox.network.";
        }
        {
          # a flush also deletes Incus's tables: every guest would run unfiltered until Incus re-applies them
          assertion = !nft.flushRuleset;
          message = "services.sacabox: networking.nftables.flushRuleset would delete Incus's tables (guests' network policies) on every firewall reload. Declare your rules with networking.nftables.tables instead of a raw ruleset (services.sacabox.nftables.exclusive keeps the ruleset to the configuration), or set networking.nftables.flushRuleset = false.";
        }
        {
          assertion = !cfg.nftables.exclusive || (nft.ruleset == "" && nft.rulesetFile == null);
          message = "services.sacabox.nftables.exclusive: a raw networking.nftables.ruleset/rulesetFile can't be told apart from foreign tables; declare it with networking.nftables.tables, or set services.sacabox.nftables.exclusive = false.";
        }
      ]
      ++ lib.mapAttrsToList
        (mname: m: {
          assertion = m.network.dns == null;
          message = "services.sacabox.machines.${mname}: network.dns was removed: restricted machines never get DNS from the host; set network.nameservers to the resolvers the guest should use.";
        })
        cfg.machines
      ++ lib.mapAttrsToList
        (tname: t: {
          assertion = !(t ? network && t.network ? dns);
          message = "services.sacabox.templates.${tname}: network.dns was removed; set network.nameservers instead.";
        })
        cfg.templates
      ++ lib.mapAttrsToList
        (mname: m: {
          assertion = !(m.init.script != null && m.init.text != "");
          message = "services.sacabox.machines.${mname}: set init.script or init.text, not both";
        })
        cfg.machines;

    warnings = lib.concatLists (lib.mapAttrsToList
      (mname: m: lib.optional (lib.elem m.network.policy [ "internet" "allowlist" ] && m.network.nameservers == [ ])
        "services.sacabox.machines.${mname}: network.policy = \"${m.network.policy}\" gets no DNS from the host and has no network.nameservers, so it can't resolve names.")
      cfg.machines);

    virtualisation.incus = {
      enable = true;
      ui.enable = cfg.ui;

      preseed = {
        networks = [
          {
            name = cfg.network.name;
            type = "bridge";
            config = {
              "ipv4.address" = cfg.network.subnet;
              "ipv4.nat" = "true";
              "ipv6.address" = "none";
            };
          }
          {
            name = cfg.network.restricted.name;
            type = "bridge";
            config = {
              "ipv4.address" = cfg.network.restricted.subnet;
              "ipv4.nat" = "true";
              "ipv6.address" = "none";
              # DHCP hands out addresses only, no DNS server: guests use their own `nameservers`.
              "raw.dnsmasq" = "dhcp-option=6";
            };
          }
        ];

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
    # Per-machine locks, shared by interactive `sacabox` and (later) the systemd units.
    systemd.tmpfiles.rules = [ "d /run/sacabox 2770 root incus-admin -" ];
    # Containers sharing host dirs map host uid/gid 1000 straight through (raw.idmap "both 1000 1000").
    users.users.root.subUidRanges = [{ startUid = 1000; count = 1; }];
    users.users.root.subGidRanges = [{ startGid = 1000; count = 1; }];

    networking.nftables.enable = lib.mkDefault true;

    systemd.services.sacabox-nftables-prune = mkIf cfg.nftables.exclusive {
      description = "Delete nftables tables that aren't in the configuration";
      after = [ "nftables.service" "incus.service" ];
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${pruneTables}/bin/sacabox-nftables-prune";
      };
    };
    # Every switch, not only when the firewall config changed: prune foreign tables and reload the
    # declared ones (atomic), so hand-added rules never survive a rebuild.
    system.activationScripts.sacabox-nftables = mkIf cfg.nftables.exclusive {
      supportsDryActivation = true;
      text = ''
        if [ -n "''${NIXOS_ACTION:-}" ] && [ -d /run/nixos ]; then
          list=/run/nixos/activation-restart-list
          [ "$NIXOS_ACTION" = dry-activate ] && list=/run/nixos/dry-activation-restart-list
          printf '%s\n' nftables.service sacabox-nftables-prune.service >> "$list"
        fi
      '';
    };
    networking.firewall.trustedInterfaces = [ cfg.network.name ];
    # Restricted guests reach the host for DHCP only (their ACLs block the rest as well).
    networking.firewall.interfaces.${cfg.network.restricted.name}.allowedUDPPorts = [ 67 ];
    networking.networkmanager.unmanaged = [
      "interface-name:${cfg.network.name}" "interface-name:${cfg.network.restricted.name}" "interface-name:tap*"
    ];
    # Incus always accepts DNS to the bridge address for NICs with ACLs; a drop in any base chain
    # on the hook is final, so this keeps the host's resolver out of reach of restricted guests.
    networking.nftables.tables.sacabox = {
      family = "inet";
      content = ''
        chain input {
          type filter hook input priority filter; policy accept;
          iifname "${cfg.network.restricted.name}" meta l4proto { tcp, udp } th dport 53 drop
        }
      '';
    };

    boot.kernelModules = [ "vhost_vsock" "vsock" ];
  };
}
