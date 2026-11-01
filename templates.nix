# Built-in templates. Whole-template priority is `mkDefault`, so redefining the
# same name elsewhere replaces it (use a new name to keep both).
{ lib, ... }:

{
  services.sacabox.templates = lib.mapAttrs (_: lib.mkDefault) {
    # Daily-driver dev box: full network, nested containers allowed.
    dev = {
      type = "vm";
      resources = { cpu = 4; memory = "8GiB"; disk = "40GiB"; };
      nesting = true;
      color = "#1f5fbf";
      terminal.settings.colors.primary.background = "#10182b";
      network.policy = "open";
    };

    # Untrusted AI agent: internet only (LAN + host blocked), no ssh keys, no shares by default.
    agent = {
      type = "vm";
      resources = { cpu = 4; memory = "8GiB"; disk = "30GiB"; };
      network.policy = "internet";
      sshAgent = false;
      color = "#2e7d32";
      terminal.settings.colors.primary.background = "#102416";
    };

    # Production access: small, gets your ssh-agent, nothing from the host filesystem.
    ops = {
      type = "vm";
      resources = { cpu = 2; memory = "4GiB"; disk = "20GiB"; };
      network.policy = "open";
      sshAgent = true;
      color = "#b3261e";
      terminal.settings.colors.primary.background = "#2b1212";
    };
  };
}
