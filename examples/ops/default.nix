# Ops box: only the public internet (plus listed ranges), no host files, own ssh-agent.   sacabox sh ops
{ inputs, ... }:

{
  services.sacabox.machines.ops = {
    template = "ops";
    os = "nixos";
    shell = "fish";
    nixos.modules = [ ./guest.nix ];
    # home.modules = inputs.dotfiles.homeModules.terminal;   # Home Manager modules, built on the host
    sshAgent = false;
    snapshot = false;
    network.policy = "internet";
    # network.allow = [ { destination = "10.20.0.0/24"; ports = "22"; } ];
  };
}
