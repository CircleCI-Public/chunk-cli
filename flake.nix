{
  description = "smelt: a self-hosted software factory for AI-built code";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        packages = {
          default = self.packages.${system}.smelt;

          smelt = pkgs.callPackage ./pkgs/smelt { };
        };

        apps = {
          default = self.apps.${system}.smelt;

          smelt = {
            type = "app";
            program = "${self.packages.${system}.smelt}/bin/smelt";
          };
        };

        # Development shell
        devShells.default = pkgs.mkShell {
          buildInputs = [ self.packages.${system}.smelt ];
        };
      }
    );
}
