#!/usr/bin/env python3
"""Generate SonDNS's font-independent SVG, PNG and Windows DIB ICO.

Requires Pillow. Run from any directory: python scripts/generate-icon.py
The letter is an explicit vector outline, so the output needs no system font.
"""

from pathlib import Path
import struct

from PIL import Image, ImageDraw


ASSETS = Path(__file__).resolve().parent.parent / "assets"
SIZES = (16, 20, 24, 32, 40, 48, 64, 128, 256)
# Coordinates use a 256 x 256 viewbox. The heavier outline remains legible at 16px.
OUTLINE = (
    ("M", 178, 76),
    ("C", 164, 61, 145, 53, 123, 53),
    ("C", 88, 53, 66, 73, 66, 102),
    ("C", 66, 126, 82, 139, 117, 149),
    ("C", 140, 156, 154, 161, 154, 172),
    ("C", 154, 183, 143, 190, 127, 190),
    ("C", 107, 190, 90, 181, 78, 168),
    ("L", 56, 192),
    ("C", 74, 212, 99, 222, 127, 222),
    ("C", 165, 222, 189, 201, 189, 170),
    ("C", 189, 144, 170, 130, 135, 120),
    ("C", 112, 114, 101, 109, 101, 99),
    ("C", 101, 89, 110, 84, 124, 84),
    ("C", 141, 84, 154, 91, 167, 103),
    ("Z",),
)


def flattened_outline():
    points = []
    current = None
    for command in OUTLINE:
        kind, *coords = command
        if kind in ("M", "L"):
            current = tuple(coords)
            points.append(current)
        elif kind == "C":
            p0 = current
            p1, p2, p3 = coords[:2], coords[2:4], coords[4:6]
            for step in range(1, 65):
                t = step / 64
                u = 1 - t
                points.append(tuple(
                    u**3 * p0[axis] + 3 * u**2 * t * p1[axis]
                    + 3 * u * t**2 * p2[axis] + t**3 * p3[axis]
                    for axis in (0, 1)
                ))
            current = tuple(p3)
    # Optical center: the S is slightly wider to the left and heavier at its base.
    return [(x + 5, y - 9) for x, y in points]


def render(size):
    factor = 4
    pixels = size * factor
    scale = pixels / 256
    mask = Image.new("L", (pixels, pixels))
    ImageDraw.Draw(mask).rounded_rectangle(
        (12 * scale, 12 * scale, 244 * scale, 244 * scale),
        radius=48 * scale,
        fill=255,
    )
    image = Image.new("RGBA", (pixels, pixels))
    draw = ImageDraw.Draw(image)
    top, bottom = (48, 127, 248), (29, 78, 216)
    for y in range(pixels):
        ratio = y / (pixels - 1)
        color = tuple(round(a + (b - a) * ratio) for a, b in zip(top, bottom))
        draw.line((0, y, pixels, y), fill=(*color, 255))
    image.putalpha(mask)
    draw.polygon([(x * scale, y * scale) for x, y in flattened_outline()], fill="white")
    return image.resize((size, size), Image.Resampling.LANCZOS)


def encode_dib(image):
    """32-bit BGRA bitmap + 1-bit AND mask, accepted by Windows and rsrc."""
    size = image.width
    pixels = image.load()
    bitmap = bytearray()
    mask = bytearray()
    mask_stride = ((size + 31) // 32) * 4
    for y in reversed(range(size)):
        mask_row = bytearray(mask_stride)
        for x in range(size):
            red, green, blue, alpha = pixels[x, y]
            bitmap.extend((blue, green, red, alpha))
            if alpha == 0:
                mask_row[x // 8] |= 1 << (7 - x % 8)
        mask.extend(mask_row)
    header = struct.pack(
        "<IiiHHIIiiII", 40, size, size * 2, 1, 32, 0, len(bitmap), 0, 0, 0, 0
    )
    return header + bitmap + mask


def write_ico(images, path):
    bitmaps = [encode_dib(image) for image in images]
    offset = 6 + 16 * len(images)
    entries = bytearray()
    for image, bitmap in zip(images, bitmaps):
        dimension = image.width if image.width < 256 else 0
        entries.extend(struct.pack(
            "<BBBBHHII", dimension, dimension, 0, 0, 1, 32, len(bitmap), offset
        ))
        offset += len(bitmap)
    path.write_bytes(struct.pack("<HHH", 0, 1, len(images)) + entries + b"".join(bitmaps))


def main():
    path = " ".join(command[0] + " ".join(map(str, command[1:])) for command in OUTLINE)
    svg = f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256">
  <title>SonDNS</title>
  <defs>
    <linearGradient id="blue" x1="0" y1="0" x2="0" y2="1">
      <stop stop-color="#307ff8"/>
      <stop offset="1" stop-color="#1d4ed8"/>
    </linearGradient>
  </defs>
  <rect x="12" y="12" width="232" height="232" rx="48" fill="url(#blue)"/>
  <path d="{path}" transform="translate(5 -9)" fill="#fff"/>
</svg>
'''
    ASSETS.mkdir(exist_ok=True)
    (ASSETS / "sondns.svg").write_text(svg, encoding="utf-8")
    images = [render(size) for size in SIZES]
    images[-1].save(ASSETS / "sondns.png")
    write_ico(images, ASSETS / "sondns.ico")
    # Useful for reviewing the actual native titlebar/taskbar resolutions.
    preview = Image.new("RGB", (384, 128), "#f4f6f9")
    x = 16
    for image in images[:7]:
        preview.paste(image, (x, (128 - image.height) // 2), image)
        x += image.width + 14
    preview.save(ASSETS / "sondns-icon-sizes.png")
    print(f"Generated SVG, PNG and {len(images)} DIB icon sizes in {ASSETS}")


if __name__ == "__main__":
    main()
