# The `sacabox` command (Go, see cmd/ and internal/). Everything declarative comes from
# /etc/sacabox/machines.json; the binary itself doesn't depend on the host config.
{ lib, pkgs, incus }:

pkgs.buildGoModule {
  pname = "sacabox";
  version = "0.1.0";

  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [ ./go.mod ./go.sum ./cmd ./internal ];
  };
  vendorHash = "sha256-PplPsiheigJpmZ/+YVUyRQVHzjCSkNNd52MbWcDwfM0=";

  subPackages = [ "cmd/sacabox" ];
  env.CGO_ENABLED = 0;
  # Absolute tool paths, so the binary works the same from a shell, a terminal window or systemd.
  ldflags = [
    "-s" "-w"
    "-X sacabox/internal/bin.Incus=${incus}/bin/incus"
    "-X sacabox/internal/bin.Nix=${pkgs.nix}/bin/nix"
    "-X sacabox/internal/bin.NixStore=${pkgs.nix}/bin/nix-store"
  ];

  meta = {
    description = "Declarative Incus machines for dev / agent / ops work";
    license = lib.licenses.mit;
    mainProgram = "sacabox";
  };
}
