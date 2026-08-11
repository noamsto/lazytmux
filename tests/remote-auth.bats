#!/usr/bin/env bats

setup() {
	# shellcheck source=/dev/null
	source "${BATS_TEST_DIRNAME}/../scripts/lib-remote.sh"
}

@test "remote_auth_identity: picks the host's own key and expands ~" {
	HOME=/home/tester remote_auth_identity "user noams
hostname mbp-m4-pro
identityfile ~/.ssh/id_ed25519
controlpath /home/tester/.ssh/master-noams@mbp:22"
	[ "$REPLY" = "/home/tester/.ssh/id_ed25519.pub" ]
}

@test "remote_auth_identity: first identityfile wins when ssh lists several" {
	HOME=/home/tester remote_auth_identity "identityfile ~/.ssh/id_ed25519
identityfile ~/.ssh/noam_factify_ed25519"
	[ "$REPLY" = "/home/tester/.ssh/id_ed25519.pub" ]
}

@test "remote_auth_identity: absolute paths pass through untouched" {
	HOME=/home/tester remote_auth_identity "identityfile /etc/keys/shared_ed25519"
	[ "$REPLY" = "/etc/keys/shared_ed25519.pub" ]
}

# No identityfile means ssh would fall back to its built-in defaults. Guessing
# one here could push a work key to a personal host, so the caller must skip the
# offer instead.
@test "remote_auth_identity: empty when the host declares no identity" {
	HOME=/home/tester remote_auth_identity "user noams
hostname lab"
	[ -z "$REPLY" ]
}
