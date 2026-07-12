{
  lib,
  config,
  ...
}: {
  # Companion module for lztmux remote nested-session promotion. Import on hosts
  # you ssh INTO so the forwarded LZTMUX_OUTER / TMUX_PANE env survives sshd.
  # home-manager cannot set sshd options, so this is a separate NixOS module.
  options.programs.lazytmux.remoteAcceptEnv =
    lib.mkEnableOption "accept lztmux remote-session env vars over sshd";

  config = lib.mkIf config.programs.lazytmux.remoteAcceptEnv {
    # Additive across settings.AcceptEnv entries; if the host already sets it,
    # use lib.mkAfter/list form per that host's config.
    services.openssh.settings.AcceptEnv = "LZTMUX_OUTER TMUX_PANE";
  };
}
