{
  pkgs,
  lib,
  ...
}:
pkgs.buildGoModule {
  pname = "og-generate";
  version = "0.1.0";
  src = lib.cleanSource ./.;
  vendorHash = "sha256-pbA/AlBz3cQYRTMnQ/qBPcinYOKokrBLNhkbRTq54gE=";
  ldflags = ["-s" "-w"];
  nativeBuildInputs = [pkgs.makeWrapper];
  # Go names the root package's binary after the module directory.
  #
  # --template stays required in the binary and gets its I6 default from the
  # wrapper rather than a patched source default: the flag's required-ness is
  # asserted by a test buildGoModule runs, and flag.Parse takes the last
  # occurrence, so a caller's own --template still wins over the added one.
  postInstall = ''
    mv $out/bin/generator $out/bin/og-generate
    wrapProgram $out/bin/og-generate \
      --add-flags "--template ${../config/tmux.conf.tmpl}"
  '';
  meta.mainProgram = "og-generate";
}
