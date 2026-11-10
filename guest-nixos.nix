# Generates the files pushed to /etc/nixos inside a NixOS guest:
#   configuration.nix        wrapper: incus guest profile + our user module + your modules
#   workstation-guest.nix    user, ssh keys, sudo, ssh-agent socket
#   modules/<i>[.nix]        your `nixos.modules`
# `@NAME@` is replaced with the machine name by the CLI at provision time.
{ lib, pkgs }:

let
  q = lib.strings.escapeNixString;
in
{
  spec = { isVm, user, sshKeys, nixosStateVersion, modules, channel, shell, nameservers }:
    let
      mods = lib.imap0
        (i: src:
          let isDir = builtins.readFileType src == "directory"; in
          { idx = i; inherit src isDir; imp = "./modules/${toString i}${lib.optionalString (!isDir) ".nix"}"; })
        modules;
    in
    {
      inherit channel nameservers;
      modules = map (m: { inherit (m) idx src isDir; }) mods;

      configuration = pkgs.writeText "configuration.nix" ''
        { lib, modulesPath, ... }: {
          imports = [
            (modulesPath + "/virtualisation/${if isVm then "incus-virtual-machine" else "lxc-container"}.nix")
            ./workstation-guest.nix
        ${lib.concatMapStrings (m: "    ${m.imp}\n") mods}  ];
          system.stateVersion = lib.mkDefault ${q nixosStateVersion};
        }
      '';

      guestModule = pkgs.writeText "workstation-guest.nix" ''
        { lib, pkgs, ... }: {
          networking.hostName = lib.mkDefault "@NAME@";

          users.users.${q user} = {
            isNormalUser = true;
            uid = 1000;
            extraGroups = [ "wheel" ];
            openssh.authorizedKeys.keys = [ ${lib.concatMapStringsSep " " q sshKeys} ];
          };
          ${lib.optionalString (shell != null) ''
          programs.${shell}.enable = true;
          users.users.${q user}.shell = pkgs.${shell};
          ''}
          security.sudo.wheelNeedsPassword = false;
          nix.settings = {
            experimental-features = [ "nix-command" "flakes" ];
            trusted-users = [ ${q user} ];
          };
          services.openssh.enable = lib.mkDefault true;
          ${lib.optionalString (nameservers != [ ]) ''
          networking.nameservers = [ ${lib.concatMapStringsSep " " q nameservers} ];
          ''}
          ${lib.optionalString isVm ''
          # Sessions opened with `incus exec` live in the agent's cgroup: with the
          # default OOMPolicy=stop, one OOM kill would take down the agent and every
          # shell/tmux with it. Only the killed process should go.
          systemd.services.incus-agent.serviceConfig.OOMPolicy = "continue";
          # VMs have no swap; compressed RAM gives headroom before the OOM killer.
          zramSwap.enable = lib.mkDefault true;
          ''}
          # Lets the shell prompt / terminal title show which machine this is.
          environment.variables.VM_NAME = "@NAME@";

          # Host ssh-agent forwarded by the `sshAgent` option.
          environment.shellInit = '''
            [ -S /run/ssh-agent.sock ] && export SSH_AUTH_SOCK=/run/ssh-agent.sock
          ''';
        }
      '';
    };
}
