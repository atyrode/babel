{
  description = "Babel - archival and exploration for agent session history";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      nixpkgs,
      flake-utils,
      ...
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        # The toolchain lives here rather than in atyrode/dotfiles because that
        # repository's inventory reserves runtimes and compilers for committed
        # project shells (inventory/annotations.nix, externalItems.projectOwned).
        # `restic` is the one exception: it is a runtime dependency of the
        # shipped binary on every machine, so dotfiles installs it fleet-wide
        # through the agent-tools capability.
        devShells.default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.restic
            # The web application is built with Bun and embedded into the Go
            # binary; `bun run build` must be runnable before `go build`.
            pkgs.bun
            # modernc.org/sqlite is pure Go, so the catalog needs no cgo. The
            # PostgreSQL client is for inspecting the Phase A shared catalog
            # during development against a local server.
            pkgs.postgresql
          ];

          # CGO_ENABLED=0 matches the release workflow: static binaries for
          # linux/darwin x amd64/arm64, and no dependency on a host compiler.
          env.CGO_ENABLED = "0";

          shellHook = ''
            echo "babel dev shell: go $(go version | cut -d' ' -f3), restic $(restic version | cut -d' ' -f2)"
          '';
        };
      }
    );
}
