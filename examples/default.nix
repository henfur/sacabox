# Example machines directory: one directory per machine (<name>/default.nix + guest.nix, ...).
# Copy it to your own (private) repo and import it next to this module, e.g. in a flake:
#   modules = [ "${inputs.workstation}" "${inputs.machines}" ];
# Shared files can go in ./common (not auto-imported; reference them from the machines).
{ lib, ... }:

{
  imports = lib.pipe (builtins.readDir ./.) [
    (lib.filterAttrs (n: t: t == "directory" && n != "common" && builtins.pathExists (./. + "/${n}/default.nix")))
    builtins.attrNames
    (map (n: ./. + "/${n}"))
  ];
}
