#!/usr/bin/env python3
"""Regenerate the splash cat frame deck from cat-source.mp4 (a seamless
sleeping-cat loop: breathing + drifting Z's) via ffmpeg (negate + hard
threshold) + chafa (braille, no dither). Authoring-time only; the committed
frames.txt (header row-count + concatenated frames) + cat-small.txt are what the
binary embeds.

    nix shell nixpkgs#ffmpeg nixpkgs#chafa -c python3 catgen.py

The threshold flattens the AI-art fill to a clean 2-tone silhouette so braille
renders a solid cat instead of dithered noise; chafa --dither none keeps solid
regions solid. The Z's are baked into the source (drift with the breath); the
renderer recolors every glyph, so the source is shape-only.
"""
import pathlib
import shutil
import subprocess
import tempfile

HERE = pathlib.Path(__file__).parent
SRC = HERE / "cat-source.mp4"
VF = (
    "negate,crop=iw*0.74:ih*0.96:iw*0.13:ih*0.02,"
    "format=gray,lutyuv=y='if(gt(val,100),255,0)'"
)
FPS = 10  # 100 frames over the 10s loop; smooth ~real-time playback


def chafa(png, w, h):
    out = subprocess.run(
        ["chafa", str(png), "-f", "symbols", "--symbols", "braille",
         "-c", "none", "--dither", "none", "--size", f"{w}x{h}", "-O", "0"],
        check=True, capture_output=True, text=True,
    ).stdout
    return [ln.replace("⠀", " ").rstrip() for ln in out.splitlines()]


def trim_common(frames):
    """Drop rows blank in EVERY frame (top & bottom) so dead space goes but all
    frames keep the same height — vertical registration across the deck."""
    h = max(len(f) for f in frames)
    frames = [f + [""] * (h - len(f)) for f in frames]
    blank = [all(not f[i].strip() for f in frames) for i in range(h)]
    top = 0
    while top < h and blank[top]:
        top += 1
    bot = h
    while bot > top and blank[bot - 1]:
        bot -= 1
    return [f[top:bot] for f in frames]


def main():
    tmp = pathlib.Path(tempfile.mkdtemp())
    try:
        subprocess.run(
            ["ffmpeg", "-hide_banner", "-loglevel", "error", "-i", str(SRC),
             "-vf", f"fps={FPS},{VF}", str(tmp / "f_%03d.png")],
            check=True,
        )
        pngs = sorted(tmp.glob("f_*.png"))
        frames = trim_common([chafa(p, 104, 32) for p in pngs])

        # One embedded file: a header line with the per-frame row count, then all
        # frames' rows concatenated. No separator char (a form feed / sentinel
        # would be eaten by the trailing-whitespace pre-commit hook); fixed-height
        # chunking survives it since blank rows stay as empty lines.
        h = len(frames[0])
        out = [str(h)]
        for f in frames:
            out.extend(f)
        (HERE / "frames.txt").write_text("\n".join(out) + "\n")

        small = chafa(pngs[len(pngs) // 2], 52, 16)
        while small and not small[0].strip():
            small.pop(0)
        while small and not small[-1].strip():
            small.pop()
        (HERE / "cat-small.txt").write_text("\n".join(small) + "\n")
        print(f"wrote {len(frames)} frames + cat-small.txt")
    finally:
        shutil.rmtree(tmp)


main()
