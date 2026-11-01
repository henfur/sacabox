# Host-level options. Per-machine options live in machine.nix.
{ config, lib, pkgs, ... }:

let
  cfg = config.services.sacabox;
  inherit (lib) mkOption mkEnableOption types;
in
{
  options.services.sacabox = {
    enable = mkEnableOption "declarative Incus dev/agent/ops machines";

    user = mkOption {
      type = types.str;
      description = "Host user: gets incus-admin, and is created (uid 1000) inside every guest.";
    };

    sshKeys = mkOption {
      type = types.listOf types.str; default = [ ];
      description = "Public keys authorised for the guest user.";
    };

    defaultImage = mkOption {
      type = types.str; default = "images:debian/13/cloud";
      description = "Image for `generic` machines. Use a */cloud variant so cloud-init creates your user.";
    };

    nixosImage = mkOption {
      type = types.str; default = "images:nixos/unstable";
      description = "Image for `nixos` machines.";
    };

    nixosStateVersion = mkOption {
      type = types.str; default = config.system.stateVersion;
      description = "system.stateVersion default inside NixOS guests.";
    };

    storage = {
      driver = mkOption {
        type = types.enum [ "dir" "btrfs" "zfs" "lvm" ]; default = "dir";
        description = ''"dir" works anywhere but is slow; "btrfs"/"zfs" give instant clones and snapshots.'';
      };
      source = mkOption {
        type = types.nullOr types.str; default = null;
        description = "Pool source (path/dataset/device); null = managed by Incus.";
      };
    };

    network = {
      name = mkOption { type = types.str; default = "incusbr0"; description = "Bridge machines attach to."; };
      subnet = mkOption { type = types.str; default = "10.88.0.1/24"; description = "Bridge address (host side / gateway)."; };
    };

    allowedShareRoots = mkOption {
      type = types.listOf types.str; default = [ ];
      example = [ "/home/you/projects" ];
      description = "If non-empty, a machine may only share host directories at or below one of these. Build-time check.";
    };

    home = {
      lib = mkOption {
        type = types.nullOr types.attrs; default = null;
        example = lib.literalExpression "inputs.home-manager.lib";
        description = "home-manager's `lib` (needs `homeManagerConfiguration`). Required when any machine sets `home.modules`.";
      };
      pkgs = mkOption {
        type = types.attrs; default = pkgs;
        description = "pkgs used to evaluate guest Home Manager configs (must match the host architecture).";
      };
      extraSpecialArgs = mkOption {
        type = types.attrs; default = { };
        example = lib.literalExpression "{ inherit pkgs-unstable; }";
        description = "specialArgs for every guest Home Manager config (your modules' extra arguments).";
      };
      stateVersion = mkOption {
        type = types.str; default = config.system.stateVersion;
        description = "home.stateVersion for guests.";
      };
    };

    templates = mkOption {
      type = types.attrsOf types.raw;
      default = { };
      description = ''
        Reusable partial machines (any machine option). A machine picks one with
        `template = "name"` and overrides what it likes. Also usable ad hoc: `vm tmp <template>`.
        Built-ins (dev, agent, ops) live in templates.nix; redefine a name to replace it.
      '';
    };

    machines = mkOption {
      type = types.attrsOf (types.submodule (import ./machine.nix { inherit lib pkgs; hostCfg = cfg; }));
      default = { };
      description = "Declared machines. One directory per machine (see examples/). Created lazily by `vm up <name>` / `vm sh <name>`.";
    };

    ui = mkOption { type = types.bool; default = true; description = "Incus web UI."; };
    uiHost = mkOption { type = types.str; default = "localhost"; description = "Host shown by `vm ui`."; };
  };
}
