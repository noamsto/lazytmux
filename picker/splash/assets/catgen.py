#!/usr/bin/env python3
"""Rasterize a curled sleeping cat to a density-glyph grid (Ghostty-style).

Regenerates the committed assets (not run at build time):
    python3 catgen.py 1.0  > cat.txt
    python3 catgen.py 0.55 > cat-small.txt
"""
import math
import sys

RAMP = " ·~+=*%$@"  # coverage 0..1 -> glyph
SCALE = float(sys.argv[1]) if len(sys.argv) > 1 else 1.0
W, H = int(58 * SCALE), int(26 * SCALE)


def inside(X, Y):
    """1 if visual point (X,Y) is inside the cat, else 0. Visual Y = 2*cell y."""
    # Body: big ellipse, curled mass.
    if ((X - 26) / 19) ** 2 + ((Y - 30) / 13) ** 2 <= 1:
        d = 1
    else:
        d = 0
    # Head: circle overlapping body's upper right.
    if (X - 41) ** 2 + (Y - 19) ** 2 <= 9.5**2:
        d = 1
    # Ears: two triangles on the head.
    for ax, base_l, base_r in ((35.5, 32.5, 39.5), (46.5, 42.5, 50.5)):
        # apex at (ax, 7), base at y=16
        if 7 <= Y <= 16:
            t = (Y - 7) / 9
            half = (base_r - base_l) / 2 * t
            cx = ax
            if cx - half <= X <= cx + half:
                d = 1
    # Tail: ring segment hugging the body's lower-left, thick stroke, tip
    # tucking under the chin side (angles avoid running off the canvas).
    r = math.hypot(X - 26, (Y - 30))
    ang = math.degrees(math.atan2(Y - 30, X - 26)) % 360
    if 19.5 <= r <= 23.5 and 100 <= ang <= 235:
        d = 1
    # Face cutout: closed eyes + nose carved out of the head fill.
    if (X - 41) ** 2 + (Y - 19) ** 2 <= 9.5**2:
        # eyes: two horizontal slits
        if 20 <= Y <= 21.6 and (35.5 <= X <= 38.5 or 43.5 <= X <= 46.5):
            d = 0
        # nose/mouth dot
        if 24 <= Y <= 25.4 and 40 <= X <= 42:
            d = 0
    return d


def coverage(cx, cy, n=4):
    """Fraction of n*n subsamples of cell (cx,cy) inside the shape."""
    hits = 0
    for i in range(n):
        for j in range(n):
            X = (cx + (i + 0.5) / n) / SCALE
            Y = (cy + (j + 0.5) / n) * 2 / SCALE
            hits += inside(X, Y)
    return hits / (n * n)


rows = []
for y in range(H):
    row = []
    for x in range(W):
        c = coverage(x, y)
        row.append(RAMP[min(int(c * (len(RAMP) - 1) + 0.5), len(RAMP) - 1)])
    rows.append("".join(row).rstrip())

# z z z drifting up-right from the head
art = rows[:]
zs = [(int(50 * SCALE), max(0, int(1 * SCALE)), "z"),
      (int(47 * SCALE), max(1, int(2 * SCALE)), "z"),
      (int(44 * SCALE), max(2, int(3 * SCALE)), "z")]
for x, y, ch in zs:
    line = art[y].ljust(x + 1)
    art[y] = line[:x] + ch + line[x + 1:]

print("\n".join(line.rstrip() for line in art).rstrip("\n"))
