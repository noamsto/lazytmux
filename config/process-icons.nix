# Process name → Nerd Font / emoji icon mapping
# Used by tmux scripts to show per-window process icons.
# Add entries as: "process-name" = "icon";
#
# Find icons at: https://www.nerdfonts.com/cheat-sheet
# Codepoint reference: https://github.com/joshmedeski/tmux-nerd-font-window-name/blob/main/bin/defaults.yml
{
  # Custom / project-specific
  claude = "🧠";
  amp = "⚡";
  nh = "❄️";
  "process-compose" = "⚙️";

  # Shells (fish excluded — default shell, too noisy)
  bash = "";
  nu = "";
  zsh = "";
  tcsh = "";

  # Editors
  emacs = "";
  hx = "";
  lvim = "";
  nano = "";
  nvim = "";
  vi = "";
  vim = "";

  # Version control
  git = "";
  gh = "";
  gitui = "";
  lazygit = "";
  lazyjj = "";
  jj = "";
  tig = "";

  # Languages & runtimes
  cargo = "";
  deno = "";
  go = "";
  java = "";
  node = "";
  perl = "";
  php = "";
  python = "";
  python3 = "";
  "Python" = "";
  ruby = "";
  rustc = "";
  rustup = "";
  scala = "";
  swift = "";
  zig = "";

  # Package managers
  apt = "";
  brew = "";
  dnf = "";
  nix = "❄️";
  npm = "";
  pnpm = "";
  pacman = "";
  paru = "";
  pip = "";
  pip3 = "";
  yarn = "";
  yay = "";

  # Build tools
  cmake = "";
  gcc = "";
  gradle = "";
  just = "";
  make = "";
  bazel = "";

  # Containers & cloud
  docker = "";
  helm = "󱃾";
  k9s = "󱃾";
  kubectl = "󱃾";
  lazydocker = "";
  terraform = "ﲽ";
  aws = "";
  gcloud = "";

  # System & monitoring
  btm = "";
  btop = "";
  htop = "";
  top = "";
  glances = "";
  sudo = "";
  systemctl = "";

  # Network
  curl = "";
  ping = "";
  ssh = "󰣀";
  scp = "󰣀";
  wget = "";
  gping = "";

  # Databases
  mongo = "";
  mysql = "";
  psql = "";
  lazysql = "";
  redis = "";
  sqlite = "";

  # File managers & tools
  bat = "󰭟";
  lf = "";
  ranger = "";
  yazi = "";
  rsync = "";
  zip = "";
  unzip = "";

  # Terminals & multiplexers
  tmux = "";
  screen = "";

  # Other
  gpg = "";
  ghostty = "";
  topgrade = "";
  weechat = "";
}
