# Sandbox for AI coding agents: NixOS inside, internet only, one scratch dir.   sacabox sh agent
_:

{
  services.sacabox.machines.agent = {
    template = "agent";
    os = "nixos";
    nixos.modules = [ ./guest.nix ];
    shares.work = { host = "/home/you/agent-work"; guest = "/home/you/work"; };
    # Restricted machines get no DNS from the host: name the resolvers the guest uses
    # (allowed through its policy automatically). Any resolver reachable from the guest works.
    network.nameservers = [ "9.9.9.9" ];
    # network.allow = [ { destination = "192.168.1.50/32"; ports = "11434"; } ];   # e.g. a local LLM
  };
}
