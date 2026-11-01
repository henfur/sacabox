# Plain NixOS module, evaluated inside the agent VM.
{ pkgs, ... }:

{
  environment.systemPackages = with pkgs; [ git ripgrep fd jq nodejs python3 ];
}
