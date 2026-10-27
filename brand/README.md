# Zimam brand files

The logo is the name in both scripts: the Arabic زِمَام, with its harakat (vowel marks), and the Latin "zimam". Navy is for the letters. Emerald is for how the name is pronounced: the harakat and the Latin spelling.

## Files

| File | Use |
| --- | --- |
| `zimam-logo.svg`, `.png` | Stacked logo on light backgrounds. |
| `zimam-logo-reversed.svg`, `.png` | Stacked logo on navy or dark backgrounds. |
| `zimam-logo-horizontal.svg`, `.png` | One-line logo for headers and the panel's brand slot, on light backgrounds. |
| `zimam-logo-horizontal-reversed.svg`, `.png` | The one-line logo on navy or dark backgrounds. |
| `zimam-icon.svg`, `zimam-icon-512.png`, `zimam-icon-192.png` | App icon: the Arabic name in a rounded navy tile. |
| `favicon.svg`, `favicon.ico` | Browser tab icon (16, 32 and 48 px in the ICO). The navy tile keeps it visible on light and dark tabs. |
| `apple-touch-icon.png` | 180 px home-screen icon. Square and opaque on purpose: iOS rounds the corners itself. |

The SVGs are outlines, so they need no font installed. The PNGs are 1200 px wide with transparent backgrounds.

## Colours

| Colour | On light | On navy |
| --- | --- | --- |
| Letters | `#0B2545` | `#F8FAFC` |
| Harakat and Latin name | `#059669` | `#34D399` |

## Details worth keeping

- The icon uses the whole Arabic name, not the letter ز alone: in this Kufi style a lone zay reads as a Latin "j" to anyone who doesn't read Arabic.
- In the icon and the one-line logo, the harakat are drawn slightly thicker than in the font, so they survive at 32–40 px. The stacked logo, meant for larger sizes, uses the font's own marks.
- Zay's dot is part of the letter, so it stays in the letter colour. Only the fatha and kasra are emerald.

## Rebuilding

The files are generated from `source/ReemKufi-wght.ttf`, Reem Kufi by Khaled Hosny and Santiago Orozco, under the SIL Open Font License 1.1 (`source/OFL.txt`). The lockups use weight 500 and the icon weight 700.

```bash
pip install fonttools uharfbuzz
python brand/source/build.py
```

PNG and ICO output also needs ImageMagick (`magick`). Sizes, weights and colours are constants at the top of `source/build.py`.
