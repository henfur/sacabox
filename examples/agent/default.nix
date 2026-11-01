# Sandbox for AI coding agents: NixOS inside, internet only, one scratch dir.   sacabox sh agent
_:

{
  services.sacabox.machines.agent = {
    template = "agent";
    os = "nixos";
    nixos.modules = [ ./guest.nix ];
    shares.work = { host = "/home/you/agent-work"; guest = "/home/you/work"; };
    # network.allow = [ { destination = "192.168.1.50/32"; ports = "11434"; } ];   # e.g. a local LLM
  };
}
