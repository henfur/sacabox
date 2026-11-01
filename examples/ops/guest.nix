# Runs inside the ops VM.
{ config, lib, pkgs, ... }:

let
  user = lib.head (lib.attrNames (lib.filterAttrs (_: u: u.isNormalUser) config.users.users));
in
{
  environment.systemPackages = with pkgs; [ opentofu ansible openssh git jq rsync curl ];

  # Private ssh-agent that lives and dies with this VM (keys never leave it).
  systemd.services.ssh-agent = {
    description = "ssh-agent for ${user}";
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      User = user;
      RuntimeDirectory = "ssh-agent";
      RuntimeDirectoryMode = "0700";
      ExecStart = "${pkgs.openssh}/bin/ssh-agent -D -a /run/ssh-agent/agent.sock";
      Restart = "on-failure";
    };
  };
  environment.variables.SSH_AUTH_SOCK = "/run/ssh-agent/agent.sock";
  programs.ssh.extraConfig = "Host *\n  AddKeysToAgent yes\n";
}
