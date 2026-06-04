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
