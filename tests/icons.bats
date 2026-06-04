#!/usr/bin/env bats

load helper

setup() {
	setup_lib_icons
}

@test "measure_display_width: pure ASCII counts one cell each" {
	measure_display_width "ENG-1957"
	[ "$REPLY_DW" = "8" ]
}

@test "measure_display_width: nerd font PUA glyph is one cell" {
	# U+F0C0D md-alpha_l_circle
	measure_display_width $'\Uf0c0d'
	[ "$REPLY_DW" = "1" ]
}

@test "measure_display_width: supplementary-plane emoji is two cells" {
	measure_display_width $'\U0001f9e0' # brain emoji
	[ "$REPLY_DW" = "2" ]
}

@test "measure_display_width: glyph + space + ascii sums correctly" {
	# U+F0C0D (1) + space (1) + "ENG-1957" (8) = 10
	measure_display_width $'\Uf0c0d ENG-1957'
	[ "$REPLY_DW" = "10" ]
}

@test "measure_display_width: empty string is zero" {
	measure_display_width ""
	[ "$REPLY_DW" = "0" ]
}

@test "truncate_to_width: short string unchanged" {
	truncate_to_width "abc" 10
	[ "$REPLY" = "abc" ]
}

@test "truncate_to_width: exact fit unchanged" {
	truncate_to_width "abcde" 5
	[ "$REPLY" = "abcde" ]
}

@test "truncate_to_width: truncates with ellipsis" {
	truncate_to_width "abcdefgh" 5
	[ "$REPLY" = "abcd…" ]
}

@test "truncate_to_width: glyph counts as one cell" {
	# U+F0C0D (1) + space (1) + a (1) = 3, then … = width 4
	truncate_to_width $'\Uf0c0d abcdef' 4
	[ "$REPLY" = $'\Uf0c0d a…' ]
}
