#!/usr/bin/env bash
# Static checks, runnable locally and in CI: ./ci/check.sh [syntax|lint|eval|go|shell|secrets]
set -euo pipefail
cd "$(dirname "$0")/.."

rev=8ce73368d1efd4d657e9064d29b144a52046b4e8
nixpkgs="https://github.com/NixOS/nixpkgs/archive/$rev.tar.gz"
nix_shell() { nix shell --extra-experimental-features 'nix-command flakes' "github:NixOS/nixpkgs/$rev#$1" --command "${@:2}"; }

nix_files() { git ls-files '*.nix'; }

syntax() {
  nix_files | xargs -r -n1 nix-instantiate --parse >/dev/null
}

lint() {
  nix_shell statix statix check .
  nix_shell deadnix deadnix --fail --no-lambda-pattern-names .
}

eval_examples() {
  nix-instantiate --eval --strict -I "nixpkgs=$nixpkgs" -E '
    let
      pkgs = import <nixpkgs> {};
      m = pkgs.lib.evalModules { modules = [ ./options.nix ./templates.nix ./examples
        { _module.args = { inherit pkgs; inputs.dotfiles.homeModules.terminal = []; }; } ]; };
    in builtins.attrNames m.config.services.sacabox.machines' >/dev/null
}

go_checks() {
  local unformatted
  unformatted=$(nix_shell go gofmt -l cmd internal)
  [ -z "$unformatted" ] || { echo "gofmt needed: $unformatted" >&2; return 1; }
  nix_shell go go vet ./...
  nix_shell go go test ./...
  # the package build also catches a stale vendorHash after go.mod changes
  nix-build --no-out-link -I "nixpkgs=$nixpkgs" -E '
    let pkgs = import <nixpkgs> {}; in
    import ./cli.nix { inherit (pkgs) lib pkgs; incus = pkgs.incus; }' >/dev/null
}

shell() {
  nix_shell shellcheck shellcheck ci/check.sh
}

secrets() {
  nix_shell gitleaks gitleaks git --redact --verbose .
  nix_shell gitleaks gitleaks dir --redact --verbose .
}

run() { echo "==> $1"; "${2:-$1}"; }

case ${1:-all} in
  syntax)  run syntax ;;
  lint)    run lint ;;
  eval)    run eval eval_examples ;;
  go)      run go go_checks ;;
  shell)   run shell ;;
  secrets) run secrets ;;
  all)     run syntax; run lint; run eval eval_examples; run go go_checks; run shell; run secrets ;;
  *)       echo "usage: $0 [syntax|lint|eval|go|shell|secrets]" >&2; exit 2 ;;
esac
