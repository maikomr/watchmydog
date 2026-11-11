#!/usr/bin/env python3
"""Render the watchmydog app icons (a paw print) as PNGs, stdlib only.

Run from web/:  python3 -I scripts/make-icons.py
Writes public/icons/{icon-192,icon-512,icon-maskable-512,favicon-64}.png.
The output is committed; re-run only when the design changes.
"""
import struct
import sys
import zlib
from pathlib import Path

ACCENT = (0x3D, 0x7E, 0xFF)  # matches --accent in style.css
WHITE = (0xFF, 0xFF, 0xFF)

# Paw geometry in unit coordinates (0..1), centred on (0.5, 0.5):
# one main pad and four toes, all ellipses (cx, cy, rx, ry).
PAW = [
    (0.50, 0.63, 0.205, 0.165),
    (0.285, 0.43, 0.075, 0.095),
    (0.415, 0.305, 0.08, 0.10),
    (0.585, 0.305, 0.08, 0.10),
    (0.715, 0.43, 0.075, 0.095),
]


def in_paw(x, y, scale):
    # Shrink the paw around the centre by `scale`.
    px = 0.5 + (x - 0.5) / scale
    py = 0.5 + (y - 0.5) / scale
    for cx, cy, rx, ry in PAW:
        dx, dy = (px - cx) / rx, (py - cy) / ry
        if dx * dx + dy * dy <= 1.0:
            return True
    return False


def in_rounded_square(x, y, radius):
    # Square 0..1 with rounded corners of the given radius.
    cx = min(max(x, radius), 1 - radius)
    cy = min(max(y, radius), 1 - radius)
    return (x - cx) ** 2 + (y - cy) ** 2 <= radius * radius


def render(size, maskable, ss=3):
    """Returns RGBA rows. Maskable icons are full-bleed with the paw inside
    the 80% safe zone; regular icons are a rounded square on transparency."""
    paw_scale = 0.62 if maskable else 0.78
    rows = []
    n = ss * ss
    for j in range(size):
        row = bytearray()
        for i in range(size):
            bg = paw = 0
            for sj in range(ss):
                for si in range(ss):
                    x = (i + (si + 0.5) / ss) / size
                    y = (j + (sj + 0.5) / ss) / size
                    inside = True if maskable else in_rounded_square(x, y, 0.22)
                    if inside:
                        bg += 1
                        if in_paw(x, y, paw_scale):
                            paw += 1
            if bg == 0:
                row += bytes((0, 0, 0, 0))
                continue
            t = paw / bg  # paw coverage within the background
            rgb = tuple(round(ACCENT[k] * (1 - t) + WHITE[k] * t) for k in range(3))
            row += bytes((*rgb, round(255 * bg / n)))
        rows.append(bytes(row))
    return rows


def write_png(path, size, rows):
    raw = b"".join(b"\x00" + r for r in rows)  # filter type 0 per row

    def chunk(kind, data):
        c = struct.pack(">I", len(data)) + kind + data
        return c + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)

    png = b"\x89PNG\r\n\x1a\n"
    png += chunk(b"IHDR", struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0))
    png += chunk(b"IDAT", zlib.compress(raw, 9))
    png += chunk(b"IEND", b"")
    path.write_bytes(png)


def main():
    out = Path("public/icons")
    out.mkdir(parents=True, exist_ok=True)
    for name, size, maskable in [
        ("icon-192.png", 192, False),
        ("icon-512.png", 512, False),
        ("icon-maskable-512.png", 512, True),
        ("favicon-64.png", 64, False),
    ]:
        write_png(out / name, size, render(size, maskable))
        print("wrote", out / name, file=sys.stderr)


if __name__ == "__main__":
    main()
