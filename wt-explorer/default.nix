{pkgs}:
pkgs.buildGoModule {
  pname = "wt-explorer";
  version = "0.1.0";
  src = let
    inherit (pkgs) lib;
  in
    lib.cleanSourceWith {
      src = ./.;
      filter = path: _type:
        !builtins.elem (baseNameOf path) ["default.nix"];
    };
  vendorHash = "sha256-SGXTtdZqPtlbtkDEfd8QpTry6vPhTBwkHQM5+zy5u1o=";
  ldflags = ["-s" "-w"];
}
