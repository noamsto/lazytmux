{
  pkgs,
  wt-explorer,
}:
pkgs.writeShellApplication {
  name = "wt";
  runtimeInputs = with pkgs; [git tmux gum zoxide wt-explorer];
  text = builtins.readFile ./wt.sh;
}
