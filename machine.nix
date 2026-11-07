# Machine submodule: the schema for one entry of `services.sacabox.machines.<name>`
# (and for each template, which is just a partial machine).
#
# Besides the user-facing options, every machine gets a read-only `spec`: the
# fully-resolved JSON document the `sacabox` CLI consumes (see cli.nix).
{ lib, pkgs, hostCfg }:

{ name, config, ... }:

let
  inherit (lib) mkOption types optional optionalAttrs;

  cidr = import ./lib/cidr.nix { inherit lib; };
  guest = import ./guest-nixos.nix { inherit lib pkgs; };

  # Never reachable in "internet" policy.
  nonPublic = [
    "0.0.0.0/8" "10.0.0.0/8" "100.64.0.0/10" "127.0.0.0/8" "169.254.0.0/16"
    "172.16.0.0/12" "192.168.0.0/16" "224.0.0.0/3"
  ];

  allowType = types.submodule {
    options = {
      destination = mkOption {
        type = types.str;
        example = "203.0.113.0/24";
        description = "IPv4 address or CIDR. (Incus ACLs match addresses, not hostnames.)";
      };
      ports = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "22,8000-8100";
        description = "Destination ports; null = all.";
      };
      protocol = mkOption {
        type = types.nullOr (types.enum [ "tcp" "udp" "icmp4" ]);
        default = null;
        description = "null = any (tcp when `ports` is set).";
      };
    };
  };

  net = config.network;
  # Restricted machines live on their own bridge, where the host answers no DNS (see default.nix).
  restricted = net.policy == "internet" || net.policy == "allowlist";
  bridge = if restricted then hostCfg.network.restricted else hostCfg.network;
  gateway = lib.head (lib.splitString "/" bridge.subnet);

  rule = a:
    { action = "allow"; inherit (a) destination; state = "enabled"; }
    // optionalAttrs (a.ports != null) { destination_port = a.ports; protocol = if a.protocol == null then "tcp" else a.protocol; }
    // optionalAttrs (a.ports == null && a.protocol != null) { inherit (a) protocol; };

  # The guest's own resolvers, reachable whatever the policy (private ones too).
  dnsRules = lib.concatMap
    (ns: map (proto: { action = "allow"; destination = "${ns}/32"; destination_port = "53"; protocol = proto; state = "enabled"; }) [ "udp" "tcp" ])
    net.nameservers;

  hostIngress = optional net.hostAccess { action = "allow"; source = "${gateway}/32"; state = "enabled"; };

  # Pure allow-lists: they don't depend on how Incus orders drop vs allow.
  acl =
    if net.policy == "internet" then {
      egress = map (d: { action = "allow"; destination = d; state = "enabled"; })
        (cidr.carve [ "0.0.0.0/0" ] nonPublic)
      ++ dnsRules ++ map rule net.allow;
      ingress = hostIngress;
    }
    else if net.policy == "allowlist" then {
      egress = dnsRules ++ map rule net.allow;
      ingress = hostIngress;
    }
    else null;

  isNixos = config.os == "nixos";
  isVm = config.type == "vm";

  # ---- generic guests: cloud-init ------------------------------------------
  cloudInit = "#cloud-config\n" + builtins.toJSON (lib.recursiveUpdate
    {
      users = [{
        name = hostCfg.user;
        uid = 1000;
        shell = "/bin/bash";
        sudo = "ALL=(ALL) NOPASSWD:ALL";
        groups = [ "sudo" ];
        lock_passwd = true;
        ssh_authorized_keys = hostCfg.sshKeys;
      }];
      write_files = [{
        path = "/etc/profile.d/ssh-agent.sh";
        content = "[ -S /run/ssh-agent.sock ] && export SSH_AUTH_SOCK=/run/ssh-agent.sock\n";
      }];
      package_update = false;
      manage_etc_hosts = true;
    }
    config.cloudInit.extra) + "\n";

  # Netplan-style v2 config: keep DHCP for the address, take DNS only from `nameservers`.
  cloudNetwork = builtins.toJSON {
    version = 2;
    ethernets.eth0 = {
      dhcp4 = true;
      dhcp4-overrides.use-dns = false;
      nameservers.addresses = net.nameservers;
    };
  } + "\n";

  homeActivation =
    if config.home.modules == [ ] then null
    else if hostCfg.home.lib == null then throw "services.sacabox.machines.${name}: home.modules needs services.sacabox.home.lib (inputs.home-manager.lib)"
    else if !isNixos then throw "services.sacabox.machines.${name}: home.modules requires os = \"nixos\""
    else
      (hostCfg.home.lib.homeManagerConfiguration {
        pkgs = hostCfg.home.pkgs;
        extraSpecialArgs = hostCfg.home.extraSpecialArgs // config.home.extraSpecialArgs;
        modules = config.home.modules ++ [{
          home.username = hostCfg.user;
          home.homeDirectory = "/home/${hostCfg.user}";
          home.stateVersion = lib.mkDefault hostCfg.home.stateVersion;
          programs.home-manager.enable = lib.mkDefault false;
        }];
      }).activationPackage;

  initFile =
    if config.init.script != null then config.init.script
    else if config.init.text != "" then pkgs.writeScript "vm-init" "#!/bin/sh\nset -eu\n${config.init.text}\n"
    else null;

  # Templates feed option *defaults* (not config definitions), which keeps the
  # module system out of a `config.template` -> `config` recursion.
  templateAttrs =
    if config.template == null then { }
    else hostCfg.templates.${config.template}
      or (throw "services.sacabox.machines.${name}: unknown template '${config.template}'");
  d = path: fallback: lib.attrByPath path fallback templateAttrs;
in
{
  options = {
    template = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = ''
        Name of an entry in `services.sacabox.templates`. Every value the template sets
        becomes the default for this machine; anything set here wins.
      '';
    };

    os = mkOption {
      type = types.enum [ "nixos" "generic" ];
      default = d [ "os" ] "generic";
      description = ''
        nixos: the guest is configured with standard NixOS modules (`nixos.modules`).
        generic: any other distro; configured by cloud-init plus an `init` script.
      '';
    };

    type = mkOption {
      type = types.enum [ "vm" "container" ];
      default = d [ "type" ] "vm";
      description = "vm = KVM virtual machine (strong isolation). container = system container (starts in ~1s, shares host kernel).";
    };

    image = mkOption {
      type = types.nullOr types.str;
      default = d [ "image" ] null;
      example = "images:ubuntu/24.04/cloud";
      description = "Incus image. null = `defaultImage` (generic) or `nixosImage` (nixos).";
    };

    resources = {
      cpu = mkOption { type = types.ints.positive; default = d [ "resources" "cpu" ] 2; description = "vCPUs."; };
      memory = mkOption { type = types.str; default = d [ "resources" "memory" ] "4GiB"; description = "RAM limit."; };
      disk = mkOption { type = types.str; default = d [ "resources" "disk" ] "30GiB"; description = "Root disk size."; };
    };

    autostart = mkOption {
      type = types.bool; default = d [ "autostart" ] false;
      description = "Start with the host (boot.autostart).";
    };

    recreateOnChange = mkOption {
      type = types.bool; default = d [ "recreateOnChange" ] false;
      description = ''
        Delete and recreate the machine when a setting that can't change in place changes
        (type, os, image, or network policy to or from "none"). Off: sacabox refuses and
        leaves the machine alone. The root disk and everything on it is lost on recreation.
      '';
    };

    snapshot = mkOption {
      type = types.bool; default = d [ "snapshot" ] true;
      description = "Take a pristine `clean` snapshot after first provisioning (`sacabox reset` rolls back to it).";
    };

    color = mkOption {
      type = types.nullOr types.str; default = d [ "color" ] null;
      example = "#b3261e";
      description = "Colour (#rrggbb) of the banner `sacabox sh` prints when entering/leaving the machine, and of nothing else; null = no banner.";
    };

    terminal.settings = mkOption {
      type = types.attrs; default = { };
      example = { colors.primary.background = "#2b1212"; font.size = 11.5; window.opacity = 0.95; };
      description = ''
        Alacritty settings for the dedicated window opened by `sacabox term`, as a nested
        attrset in alacritty.toml's shape (each leaf becomes an `-o key=value` override on top
        of your own config). Merged over the template's settings, so a machine only states
        what differs. Typical use: a distinct background so the machine is recognisable.
      '';
    };

    nesting = mkOption {
      type = types.bool; default = d [ "nesting" ] false;
      description = "Allow containers/VMs inside (docker, podman, incus). Containers only; VMs can always do it.";
    };

    sshAgent = mkOption {
      type = types.bool; default = d [ "sshAgent" ] false;
      description = "Proxy your session's SSH agent into the guest at /run/ssh-agent.sock (keys never copied).";
    };

    shares = mkOption {
      default = d [ "shares" ] { };
      example = { code = { host = "/home/me/projects/foo"; }; };
      description = ''
        Host directories visible in the guest. This is the *only* host filesystem the
        guest can touch. Hosts must live under `allowedShareRoots` when that is set.
      '';
      type = types.attrsOf (types.submodule {
        options = {
          host = mkOption { type = types.strMatching "/.*"; description = "Absolute host path."; };
          guest = mkOption {
            type = types.nullOr types.str; default = null;
            description = "Mount point in the guest. null = same path as on the host.";
          };
          readOnly = mkOption { type = types.bool; default = false; };
        };
      });
    };

    network = {
      policy = mkOption {
        type = types.enum [ "open" "internet" "allowlist" "none" ];
        default = d [ "network" "policy" ] "open";
        description = ''
          open:      full access (NAT through the host, can reach LAN and other machines).
          internet:  public internet only; private/LAN ranges blocked except `allow` entries.
          allowlist: nothing except `allow` entries (and `nameservers`).
          internet and allowlist machines get no DNS from the host: set `nameservers`.
          none:      no network interface at all.
        '';
      };
      allow = mkOption {
        type = types.listOf (types.coercedTo types.str (x: { destination = x; }) allowType);
        default = d [ "network" "allow" ] [ ];
        example = [ "192.168.1.50/32" { destination = "203.0.113.10/32"; ports = "22"; } ];
        description = "Destinations reachable under `internet` / `allowlist`. Plain strings are CIDRs, all ports.";
      };
      # Removed: kept only so default.nix can explain the replacement (an assertion rejects it).
      dns = mkOption {
        type = types.nullOr types.bool; default = null; visible = false;
        description = "Removed; see `nameservers`.";
      };
      nameservers = mkOption {
        type = types.listOf (types.strMatching "[0-9]+\\.[0-9]+\\.[0-9]+\\.[0-9]+");
        default = d [ "network" "nameservers" ] [ ];
        example = [ "203.0.113.53" ];
        description = ''
          DNS resolvers (IPv4) configured inside the guest, and allowed through its policy on port 53.
          Machines under `internet` / `allowlist` get no DNS from the host, so without these they
          can't resolve names. NixOS guests: `networking.nameservers`; others: cloud-init network
          config, applied when the machine is created.
        '';
      };
      hostAccess = mkOption {
        type = types.bool; default = d [ "network" "hostAccess" ] true;
        description = "Allow the host to initiate connections to the guest (ssh, dev servers) under restricted policies.";
      };
    };

    nixos = {
      modules = mkOption {
        type = types.listOf types.path;
        default = d [ "nixos" "modules" ] [ ];
        example = lib.literalExpression "[ ./guest.nix ]";
        description = ''
          Standard NixOS modules applied *inside* the guest (`nixos-rebuild switch` runs in it).
          A file is copied alone; to ship a module with relative imports pass its directory
          (it must contain default.nix).
        '';
      };
      channel = mkOption {
        type = types.str; default = d [ "nixos" "channel" ] "nixos-unstable";
        description = "Channel added inside the guest if it has no nixpkgs.";
      };
    };

    init = {
      script = mkOption {
        type = types.nullOr types.path; default = d [ "init" "script" ] null;
        description = "Executable run as root inside the guest after first boot (any interpreter; env: VM_NAME, VM_USER, VM_HOME).";
      };
      text = mkOption {
        type = types.lines; default = d [ "init" "text" ] "";
        description = "Inline /bin/sh alternative to `script`.";
      };
    };

    home = {
      modules = mkOption {
        type = types.listOf types.raw;
        default = d [ "home" "modules" ] [ ];
        example = lib.literalExpression "inputs.dotfiles.homeModules.terminal";
        description = ''
          Home Manager modules for the guest user, evaluated on the HOST (so relative paths
          and flake inputs just work) and activated in the guest with `sacabox provision` /
          `sacabox provision`. NixOS guests only. Don't mix with `dotfiles` on the same paths.
        '';
      };
      extraSpecialArgs = mkOption {
        type = types.attrs;
        default = d [ "home" "extraSpecialArgs" ] { };
        description = "Per-machine specialArgs, merged over `services.sacabox.home.extraSpecialArgs`.";
      };
    };

    shell = mkOption {
      type = types.nullOr (types.enum [ "fish" "zsh" "bash" ]);
      default = d [ "shell" ] null;
      description = "Login shell of the guest user (nixos guests: installed and set via NixOS options). null = distro default.";
    };

    dotfiles = mkOption {
      type = types.nullOr types.path;
      default = d [ "dotfiles" ] null;
      example = lib.literalExpression "./dotfiles/ops";
      description = ''
        Directory whose contents are copied into the guest user's home after provisioning
        (so `<dir>/.config/fish/config.fish` becomes `~/.config/fish/config.fish`).
        Files are overwritten on `sacabox provision`; anything else in $HOME
        (e.g. ssh keys generated in the guest) is left alone. It is copied into the nix
        store: never put secrets in it.
      '';
    };

    cloudInit.extra = mkOption {
      type = types.attrs; default = d [ "cloudInit" "extra" ] { };
      description = "Extra cloud-config merged over the generated one (generic guests).";
    };

    spec = mkOption {
      type = types.attrs; readOnly = true; internal = true;
      description = "Resolved machine description consumed by `sacabox`.";
    };
  };

  config.spec = {
        inherit (config) os type autostart snapshot nesting sshAgent color recreateOnChange;
        image =
          if config.image != null then config.image
          else if isNixos then hostCfg.nixosImage
          else hostCfg.defaultImage;
        inherit (config.resources) cpu memory disk;
        shares = lib.mapAttrsToList
          (n: s: { name = n; inherit (s) host readOnly; guest = if s.guest == null then s.host else s.guest; })
          config.shares;
        network = {
          inherit (net) policy;
          bridge = bridge.name;
          inherit acl;
          inherit (net) nameservers;
        };
        cloudInit = if isNixos then null else pkgs.writeText "cloud-init-${name}" cloudInit;
        cloudNetwork = if isNixos || net.nameservers == [ ] then null else pkgs.writeText "cloud-network-${name}" cloudNetwork;
        nixos = if !isNixos then null else guest.spec {
          inherit isVm;
          inherit (hostCfg) user sshKeys nixosStateVersion;
          inherit (config.nixos) modules channel;
          inherit (config) shell;
          inherit (net) nameservers;
        };
        terminal.settings = lib.recursiveUpdate (d [ "terminal" "settings" ] { }) config.terminal.settings;
        init = initFile;
        inherit (config) dotfiles;
        home = homeActivation;
      };
}
