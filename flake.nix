{
  description = "Babel - archival and exploration for agent session history";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
      ...
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};

        # A Nix build compiles from a source copy with no `.git`, so
        # `-buildvcs` stamps nothing and `babel version` would report no
        # provenance. The revision and snapshot timestamp therefore come from
        # the flake's own metadata and are injected with `-ldflags -X`.
        # `self.rev` is absent when the tree is dirty, and both revisions are
        # absent when the source carries no git metadata at all, hence the
        # layered fallback: `internal/cli` records "unknown" as no commit
        # rather than as a fake one.
        revision = self.rev or self.dirtyRev or "unknown";
        stamp = self.lastModifiedDate or "00000000000000";
        field = start: len: builtins.substring start len stamp;
        snapshotDate = "${field 0 4}-${field 4 2}-${field 6 2}";
        buildTime = "${snapshotDate}T${field 8 2}:${field 10 2}:${field 12 2}Z";
        # Untagged snapshot of a revision-pinned source: nixpkgs' convention
        # for a build that is not a tagged release.
        pkgVersion = "0-unstable-${snapshotDate}";
        ldflagVar = name: value: "-X github.com/atyrode/babel/internal/cli.${name}=${value}";

        babel = pkgs.buildGoModule {
          pname = "babel";
          version = pkgVersion;
          src = ./.;
          vendorHash = "sha256-7jbiWg2OlEPSQV3oai5GY38HPg/0VyXyfMbmKSZQ53w=";

          # Only the CLI is an output; every other package is library code
          # linked into it. `web/dist` is committed and embedded by
          # internal/web (`go:embed all:dist`), so the derivation ships the
          # checked-in assets and needs no frontend toolchain.
          subPackages = [ "cmd/babel" ];

          # modernc.org/sqlite is pure Go: no host compiler, static binary,
          # matching the release workflow and the dev shell.
          env.CGO_ENABLED = "0";

          ldflags = [
            "-s"
            "-w"
            (ldflagVar "buildVersion" pkgVersion)
            (ldflagVar "buildCommit" revision)
            (ldflagVar "buildTime" buildTime)
          ];

          # The suite needs a PostgreSQL server and a restic binary; it is
          # gated in CI, not in the package build.
          doCheck = false;

          meta = {
            description = "Archival and exploration for agent session history";
            homepage = "https://github.com/atyrode/babel";
            license = pkgs.lib.licenses.mit;
            mainProgram = "babel";
            platforms = pkgs.lib.platforms.linux ++ pkgs.lib.platforms.darwin;
          };
        };
      in
      {
        packages = {
          default = babel;
          inherit babel;
        };

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
