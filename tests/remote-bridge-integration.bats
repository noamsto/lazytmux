#!/usr/bin/env bats

setup() {
	SOCK="$BATS_TEST_TMPDIR/t.sock"
	BRIDGE="$BATS_TEST_TMPDIR/bridge"
	(cd "$BATS_TEST_DIRNAME/../picker" && go build -o "$BRIDGE" ./remotebridge/)
	tmux -S "$SOCK" -f /dev/null new-session -d -s src -x 80 -y 24
	tmux -S "$SOCK" send-keys -t src "printf HELLO_BRIDGE" Enter
	sleep 0.5
}

teardown() { tmux -S "$SOCK" kill-server 2>/dev/null || true; }

@test "bridge renders the remote window's current screen" {
	run timeout 5 "$BRIDGE" --ssh "" --tmux "tmux -S $SOCK" --session src --window 0 </dev/null
	[[ $output == *HELLO_BRIDGE* ]]
}
