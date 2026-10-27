"""Builds the Zimam logo files in brand/ from Reem Kufi (SIL Open Font License 1.1).

Every letter is shaped with HarfBuzz and written as vector outlines, so the
files need no font to display.

    python brand/source/build.py

Needs the Python packages fonttools and uharfbuzz. PNG and ICO files also need
ImageMagick's `magick` on the PATH.
"""

import io
import subprocess
import tempfile
from pathlib import Path

import uharfbuzz as hb
from fontTools.pens.boundsPen import BoundsPen
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont
from fontTools.varLib import instancer

SOURCE = Path(__file__).resolve().parent
OUT = SOURCE.parent
FONT = SOURCE / "ReemKufi-wght.ttf"

NAME_ARABIC = "زِمَام"
NAME_LATIN = "zimam"

LOCKUP_WEIGHT = 500
ICON_WEIGHT = 700

# Harakat (the vowel marks) take the accent colour. Zay's dot is part of the
# letter, so it stays in the ink colour.
HARAKAT = {"fatha-ar", "kasra-ar"}

ON_LIGHT = {"ink": "#0B2545", "accent": "#059669"}
ON_NAVY = {"ink": "#F8FAFC", "accent": "#34D399"}
NAVY = "#0B2545"

# Sizes relative to the Arabic word.
LATIN_STACKED = 0.30
LATIN_HORIZONTAL = 0.60

# Extra harakat thickness for the one-line logo, in font units.
HARAKAT_STROKE_HORIZONTAL = 30


def fmt(value):
    text = f"{value:.2f}".rstrip("0").rstrip(".")
    return "0" if text in ("-0", "") else text


def load(weight):
    """A static instance of the variable font, for both shaping and outlines."""
    static = instancer.instantiateVariableFont(TTFont(FONT), {"wght": weight})
    data = io.BytesIO()
    static.save(data)
    blob = data.getvalue()
    return TTFont(io.BytesIO(blob)), hb.Font(hb.Face(hb.Blob(blob)))


def arabic_role(name):
    return "accent" if name in HARAKAT else "ink"


def latin_role(name):
    return "accent"


class Run:
    """Shaped text at one scale, measured by its ink rather than font metrics."""

    def __init__(self, font, text, role, scale=1.0, tracking=0):
        self.ttfont, hbfont = font
        self.glyph_set = self.ttfont.getGlyphSet()
        self.role = role
        self.scale = scale

        buf = hb.Buffer()
        buf.add_str(text)
        buf.guess_segment_properties()
        hb.shape(hbfont, buf, {})

        # HarfBuzz returns glyphs in visual order, left to right, for both scripts.
        self.glyphs = []
        x = 0
        for info, pos in zip(buf.glyph_infos, buf.glyph_positions):
            self.glyphs.append((self.ttfont.getGlyphName(info.codepoint), x + pos.x_offset, pos.y_offset))
            if pos.x_advance:
                x += pos.x_advance + tracking

        pen = BoundsPen(self.glyph_set)
        for name, gx, gy in self.glyphs:
            self.glyph_set[name].draw(TransformPen(pen, (1, 0, 0, 1, gx, gy)))
        self.xmin, self.ymin, self.xmax, self.ymax = pen.bounds

    @property
    def width(self):
        return (self.xmax - self.xmin) * self.scale

    @property
    def height(self):
        return (self.ymax - self.ymin) * self.scale

    def baseline(self, top):
        """The baseline's y when the ink's top edge sits at `top`."""
        return top + self.ymax * self.scale

    def top_for_baseline(self, baseline):
        return baseline - self.ymax * self.scale

    def paths(self, left, top, palette, harakat_stroke=0):
        """(colour, path data, stroke width) with the ink's top-left corner at (left, top).

        `harakat_stroke`, in font units, thickens the harakat so the thin marks
        survive at small sizes.
        """
        s = self.scale
        dx = left - self.xmin * s
        dy = self.baseline(top)
        groups = {}
        for name, gx, gy in self.glyphs:
            pen = SVGPathPen(self.glyph_set, ntos=fmt)
            self.glyph_set[name].draw(TransformPen(pen, (s, 0, 0, -s, dx + gx * s, dy - gy * s)))
            groups.setdefault((self.role(name), name in HARAKAT), []).append(pen.getCommands())
        return [
            (palette[role], "".join(d), harakat_stroke * s if is_mark else 0)
            for (role, is_mark), d in groups.items()
        ]


def svg(paths, width, height, background=None, radius=0):
    lines = [
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {fmt(width)} {fmt(height)}" '
        f'width="{fmt(width)}" height="{fmt(height)}" role="img" aria-label="Zimam">',
        "<title>Zimam</title>",
    ]
    if background:
        lines.append(f'<rect width="{fmt(width)}" height="{fmt(height)}" rx="{fmt(radius)}" fill="{background}"/>')
    for colour, d, stroke in paths:
        outline = f' stroke="{colour}" stroke-width="{fmt(stroke)}" stroke-linejoin="round"' if stroke else ""
        lines.append(f'<path fill="{colour}"{outline} d="{d}"/>')
    lines.append("</svg>")
    return "\n".join(lines) + "\n"


def stacked(font, palette):
    """The Arabic name over the Latin one, centred."""
    arabic = Run(font, NAME_ARABIC, arabic_role, scale=0.1)
    latin = Run(font, NAME_LATIN, latin_role, scale=0.1 * LATIN_STACKED, tracking=60)
    width = max(arabic.width, latin.width)
    gap = arabic.height * 0.16
    paths = arabic.paths((width - arabic.width) / 2, 0, palette)
    paths += latin.paths((width - latin.width) / 2, arabic.height + gap, palette)
    return svg(paths, width, arabic.height + gap + latin.height)


def horizontal(font, palette):
    """The Arabic name, then the Latin one on the same baseline.

    Made for headers about 40 px tall, so the harakat are thickened.
    """
    arabic = Run(font, NAME_ARABIC, arabic_role, scale=0.1)
    latin = Run(font, NAME_LATIN, latin_role, scale=0.1 * LATIN_HORIZONTAL, tracking=30)
    gap = arabic.height * 0.3
    latin_top = latin.top_for_baseline(arabic.baseline(0))
    top = min(0, latin_top)
    bottom = max(arabic.height, latin_top + latin.height)
    paths = arabic.paths(0, -top, palette, HARAKAT_STROKE_HORIZONTAL)
    paths += latin.paths(arabic.width + gap, latin_top - top, palette)
    return svg(paths, arabic.width + gap + latin.width, bottom - top)


def icon(font, size=512, radius=112, fill=0.70, harakat_stroke=44):
    """The whole Arabic name in a navy tile, with the harakat thickened.

    The whole name, because a lone Kufi zay reads as a Latin "j". A radius of 0
    gives the full-bleed square iOS expects.
    """
    measure = Run(font, NAME_ARABIC, arabic_role)
    scale = fill * size / max(measure.width, measure.height)
    word = Run(font, NAME_ARABIC, arabic_role, scale=scale)
    paths = word.paths((size - word.width) / 2, (size - word.height) / 2, ON_NAVY, harakat_stroke)
    return svg(paths, size, size, background=NAVY, radius=radius)


def render(svg_path, png_path, width):
    """Rasterise with ImageMagick, drawing at twice the size and scaling down."""
    svg_width = float(svg_path.read_text(encoding="utf-8").split(' width="', 1)[1].split('"')[0])
    density = max(96, 96 * 2 * width / svg_width)
    subprocess.run(
        ["magick", "-background", "none", "-density", fmt(density), str(svg_path), "-resize", f"{width}x", "-depth", "8", str(png_path)],
        check=True,
    )


def write(name, content):
    path = OUT / name
    path.write_text(content, encoding="utf-8", newline="\n")
    return path


def main():
    lockup = load(LOCKUP_WEIGHT)
    icon_font = load(ICON_WEIGHT)

    logos = {
        "zimam-logo.svg": stacked(lockup, ON_LIGHT),
        "zimam-logo-reversed.svg": stacked(lockup, ON_NAVY),
        "zimam-logo-horizontal.svg": horizontal(lockup, ON_LIGHT),
        "zimam-logo-horizontal-reversed.svg": horizontal(lockup, ON_NAVY),
    }
    for name, content in logos.items():
        path = write(name, content)
        render(path, path.with_suffix(".png"), 1200)

    tile = write("zimam-icon.svg", icon(icon_font))
    write("favicon.svg", icon(icon_font))
    render(tile, OUT / "zimam-icon-512.png", 512)
    render(tile, OUT / "zimam-icon-192.png", 192)

    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path(tmp)
        square = tmp / "square.svg"
        square.write_text(icon(icon_font, radius=0), encoding="utf-8")
        render(square, OUT / "apple-touch-icon.png", 180)

        sizes = []
        for px in (16, 32, 48):
            sizes.append(tmp / f"favicon-{px}.png")
            render(tile, sizes[-1], px)
        subprocess.run(["magick", *map(str, sizes), str(OUT / "favicon.ico")], check=True)


if __name__ == "__main__":
    main()
