#!/usr/bin/env bash
# Usage: ./apply.sh MACHINE [provision|home|up]
#   provision  guest.nix, init script, dotfiles (default)
#   home       Home Manager modules from the dotfiles repo
#   up         resources, shares, network policy
# Env: HOST (flake host, default: this machine's hostname)
set -euo pipefail

machine=${1:?usage: apply.sh MACHINE [provision|home|up]}
action=${2:-provision}
host=${HOST:-$(hostname)}
lock=$(mktemp)

inputs=(sacabox machines)
[ "$action" = home ] && inputs+=(dotfiles)

nix flake update "${inputs[@]}" --flake path:/etc/nixos --output-lock-file "$lock"
sudo cp "$lock" /etc/nixos/flake.lock
rm -f "$lock"

sudo nixos-rebuild switch --flake "/etc/nixos#$host"
vm "$action" "$machine"
