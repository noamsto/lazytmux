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
    # AcceptEnv is a listOf str; NixOS concatenates list definitions, so this
    # merges additively with any AcceptEnv the host already sets.
    services.openssh.settings.AcceptEnv = ["LZTMUX_OUTER" "TMUX_PANE"];
    # The reverse-forwarded socket the initiator RemoteForwards here is bound by
    # THIS sshd; the client's StreamLocalBindUnlink can't govern it. Without this
    # a socket left over from an ungraceful disconnect blocks every later forward
    # ("remote port forwarding failed"), so the handshake never reaches home.
    services.openssh.settings.StreamLocalBindUnlink = true;
  };
}
