{pkgs}:
pkgs.writeShellApplication {
  name = "wt";
  runtimeInputs = with pkgs; [git tmux gum zoxide];
  text = builtins.readFile ./wt.sh;
}
