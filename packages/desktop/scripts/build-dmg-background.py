#!/usr/bin/env python3
"""Draws the DMG window background (660x400, with a @2x representation) into src-tauri/dmg/background.tiff.

Finder shows the background as a still image, so the mascot is one typing frame of the animated SVG.
The layout matches tauri.conf.json: the window is 428 high because its bounds include the 28 pt title bar.
"""
import re
import struct
import subprocess
import tempfile
import zlib
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
MASCOT = ROOT / "docs/assets/mascot-cat-animated.svg"
OUT = ROOT / "packages/desktop/src-tauri/dmg/background.tiff"

W, H = 660, 400
# Pure black: Finder has no setting to hide icon labels, but it draws them black over a picture, so they vanish here.
BACKGROUND = "#000000"
FLOOR = "#27272A"
ACCENT = "#2B7FFF"
SECONDARY = "#A1A1AA"
INK = "#000000"
# One chevron per step between the app and Applications, fading toward the folder.
CHEVRONS = [(270, 1.0), (312, 0.65), (354, 0.35)]
CHEVRON_Y, CHEVRON_CELL = 159, 6
CHEVRON_CELLS = [(0, 0), (1, 1), (2, 2), (1, 3), (0, 4)]
# The typing frame; the cat stands on the floor line.
CAT_FRAME, CAT_X, CAT_Y, CAT_CELL = "f34", 250, 276, 6
FLOOR_Y, FLOOR_H = 378, 4


def rgb(color):
    return tuple(int(color[i : i + 2], 16) for i in (1, 3, 5))


def mix(color, alpha):
    return tuple(round(c * alpha + b * (1 - alpha)) for c, b in zip(rgb(color), rgb(BACKGROUND)))


def cat_cells():
    """Yields (x, y, width, colour) runs from the frame's paths, which are all M x y h w v1 h-w z."""
    frame = re.search(rf'<g class="f {CAT_FRAME}">(.*?)</g>', MASCOT.read_text()).group(1)
    colors = {"p": ACCENT, "s": SECONDARY, "k": INK}
    for cls, d in re.findall(r'<path class="(\w)" d="([^"]+)"', frame):
        for x, y, w in re.findall(r"M(\d+) (\d+)h(\d+)", d):
            yield int(x), int(y), int(w), colors[cls]


def render(scale):
    px = [[rgb(BACKGROUND)] * (W * scale) for _ in range(H * scale)]

    def rect(x, y, w, h, color):
        for row in range(y * scale, (y + h) * scale):
            px[row][x * scale : (x + w) * scale] = [color] * (w * scale)

    rect(0, FLOOR_Y, W, FLOOR_H, rgb(FLOOR))
    for x, alpha in CHEVRONS:
        for col, row in CHEVRON_CELLS:
            rect(x + col * CHEVRON_CELL, CHEVRON_Y + row * CHEVRON_CELL, CHEVRON_CELL, CHEVRON_CELL, mix(ACCENT, alpha))
    for x, y, w, color in cat_cells():
        rect(CAT_X + x * CAT_CELL, CAT_Y + y * CAT_CELL, w * CAT_CELL, CAT_CELL, rgb(color))
    return px


def write_png(path, px, scale):
    def chunk(tag, data):
        return struct.pack(">I", len(data)) + tag + data + struct.pack(">I", zlib.crc32(tag + data))

    ppm = round(72 * scale / 0.0254)
    raw = b"".join(b"\0" + bytes(v for p in row for v in p) for row in px)
    path.write_bytes(
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", struct.pack(">IIBBBBB", len(px[0]), len(px), 8, 2, 0, 0, 0))
        + chunk(b"pHYs", struct.pack(">IIB", ppm, ppm, 1))
        + chunk(b"IDAT", zlib.compress(raw))
        + chunk(b"IEND", b"")
    )


def main():
    OUT.parent.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory() as tmp:
        one, two = Path(tmp, "background.png"), Path(tmp, "background@2x.png")
        write_png(one, render(1), 1)
        write_png(two, render(2), 2)
        subprocess.run(["tiffutil", "-cathidpicheck", str(one), str(two), "-out", str(OUT)], check=True)
    print(OUT)


if __name__ == "__main__":
    main()
