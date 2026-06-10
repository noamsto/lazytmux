#!/usr/bin/env python3
"""Regenerate the splash cat art from cat-source.png (a frame of a sleeping-cat
clip) via ffmpeg (negate) + chafa (braille). Authoring-time only; the committed
.txt files are what the binary embeds.

    nix shell nixpkgs#ffmpeg nixpkgs#chafa -c python3 catgen.py

Braille packs a 2x4 dot grid per cell, so the silhouette stays crisp at small
cell counts. chafa fills empty cells with U+2800 (braille blank); we fold those
to plain spaces so the renderer's space test and trailing-strip both work.
"""
import pathlib
import subprocess

HERE = pathlib.Path(__file__).parent
SRC = HERE / "cat-source.png"


def gen(w, h, out):
    subprocess.run(
        ["ffmpeg", "-hide_banner", "-loglevel", "error", "-i", str(SRC),
         "-vf", "negate", "-frames:v", "1", "-y", "/tmp/_catinv.png"],
        check=True,
    )
    txt = subprocess.run(
        ["chafa", "/tmp/_catinv.png", "-f", "symbols", "--symbols", "braille",
         "-c", "none", "--size", f"{w}x{h}", "-O", "0"],
        check=True, capture_output=True, text=True,
    ).stdout
    lines = [ln.replace("⠀", " ").rstrip() for ln in txt.splitlines()]
    while lines and not lines[0].strip():
        lines.pop(0)
    while lines and not lines[-1].strip():
        lines.pop()
    (HERE / out).write_text("\n".join(lines) + "\n")


gen(64, 18, "cat.txt")
gen(40, 11, "cat-small.txt")
