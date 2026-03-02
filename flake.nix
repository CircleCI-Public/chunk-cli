{
  description = "Chunk CLI by CircleCI";

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
          default = self.packages.${system}.chunk;

          chunk = pkgs.callPackage ./pkgs/chunk { };
        };

        apps = {
          default = self.apps.${system}.chunk;

          chunk = {
            type = "app";
            program = "${self.packages.${system}.chunk}/bin/chunk";
          };
        };

        # Development shell
        devShells.default = pkgs.mkShell {
          buildInputs = [ self.packages.${system}.chunk ];
        };
      }
    );
}
