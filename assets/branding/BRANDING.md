# ContainerBin branding

This package reconstructs the maintainer-approved raster identity as native
vector artwork. The concept is approved; **final vector fidelity is pending
maintainer visual acceptance**. Automated validation is not design approval.

The graphite isometric shipping container represents isolated tool runtimes.
Exactly four cyan ribs on the top and four on the visible side preserve the
shipping-container structure. The cyan terminal opening and turquoise `>_`
connect that isolation to ordinary command-line use. The name is **ContainerBin**.

## Palette

Colors were sampled from solid interior regions of the supplied PNGs, ignoring
low-alpha contamination. Small tonal variations are represented with native
SVG linear gradients, rather than raster textures.

| Use | sRGB values |
| --- | --- |
| Graphite container | `#131D28` to `#141E29` |
| Structural cyan | `#05B8F9` to `#01B5FC` |
| Terminal recess blue | `#014680` to `#034678` |
| Prompt turquoise | `#07EEE8` to `#00DFDF` |
| Light wordmark “Container” | `#111B24` |
| Dark wordmark “Container” | `#F7F9FC` |
| Wordmark “Bin” | `#01B7FD` |
| Monochrome ink | `#131D28` or `#FFFFFF` |

## Assets and usage

| Files | Purpose |
| --- | --- |
| `icon.svg` | Native, hand-reconstructed primary master; 960-square viewBox |
| `icon-monochrome.svg`, `icon-monochrome-white.svg` | One ink color with true negative space; use graphite on light backgrounds and white on dark backgrounds |
| `logo-light.svg`, `logo-dark.svg` | Native icon plus 12 outlined glyphs; 2048 × 682 canvas |
| `favicon.svg` | Native small-size derivative; same silhouette and ribs, tighter 910-square framing and prompt enlarged 18% about its center |
| `icon-{1024,512,256,64,32}.png` | RGBA exports directly from `icon.svg`, at the named square sizes |
| `favicon-{16,32}.png` | RGBA exports directly from `favicon.svg` |
| `logo-light.png`, `logo-dark.png` | RGBA exports directly from the corresponding SVG, 2048 × 682 |
| `comparison.png` | Approved references at left, vector renders at right; light, dark and checkerboard proofs, including both monochromes |
| `small-sizes.png` | 16/32px favicon and 64/256px primary icon, on three backgrounds; actual pixels and nearest-neighbor magnification |
| `references/` | The three original PNGs and their original README, retained byte-for-byte |
| `source/` | Licensed font, OFL notice and isolated build dependency locks |

Use `logo-light.svg` on white/light surfaces and `logo-dark.svg` on dark surfaces.
Both have exactly the same icon, letter outlines, positions, baseline and
spacing; only the “Container” fill changes. The repository README uses HTML
`picture` sources selected by `prefers-color-scheme`, with a light fallback,
image alternative text and a retained text heading.

All production assets have transparent backgrounds. The backgrounds in the
comparison sheets are deliberately opaque proof surfaces. Do not stretch,
rotate, add effects, recolor the full-color artwork or replace the typeface.
Keep at least 10% of the visible icon width clear around standalone uses, and
at least one cap-height around a wordmark where layout permits. Canvas padding
does not replace that surrounding clear space. Recommended primary icon size
is 32px or more, and wordmark width is 320px or more. Prefer the favicon at
16–32px. At 16px the silhouette and cyan terminal survive, but the individual
ribs and underscore are subpixel details; use 32px+ when those must read clearly.

## Source fidelity and intentional differences

The original archive is `containerbin-brand-references.zip`. Its three image
hashes are recorded in `references/README.md` and independently checked during
export. Reference dimensions are 1254 × 1254 (icon) and 2048 × 682 (wordmarks).
They are actual RGBA images, not images with a painted checkerboard.

The reconstruction is **not pixel-perfect**:

- `icon.svg` uses 13 carefully placed paths, four simple gradients and smooth
  quadratic curves. The perspective, silhouette, four-plus-four rib count,
  terminal opening, recess and prompt follow the primary reference. Tiny
  raster texture, irregular edge pixels and local tonal noise are replaced
  by continuous geometry and restrained gradients. Corner curvature and
  terminal-recess contour differ slightly from the generated raster.
- The original large icon has substantial transparent canvas padding. Its
  vector viewBox is `(150, 180, 960, 960)` in the original coordinate system.
  This changes framing, not the proportions of the drawing. The comparison
  sheet crops the reference to the same rectangle for a fair icon comparison.
- Both wordmarks use the primary icon at one common scale and placement,
  fitted to the light reference. The supplied dark reference's icon is
  slightly larger/higher and its text has a different baseline and edge
  damage. Those inconsistencies are intentionally reconciled to the shared
  light-reference layout, as required for identical theme geometry.
- The reference's exact typeface is unidentified. These letters are
  **Montserrat Black (weight 900)**, individually fitted to measured solid-ink
  bounds from `wordmark-light.png`. Horizontal and vertical affine fitting
  preserves the overall approved layout, but does not make Montserrat the
  original font. Curves, counters, stroke weight, `C` terminals, `t` foot,
  `r` shoulder, `B` bowls and `i` dots remain visibly different. This is the
  main visual acceptance question; the comparison sheets expose it directly.
- Reference interior alpha is predominantly 253–254, with stray low-alpha
  pixels outside the intended artwork. Vector fills are opaque, with genuine
  transparent surroundings and clean antialiased edges. No stray pixels or
  generated dark-wordmark halos are copied.
- Only the favicon enlarges the existing prompt by 18% and tightens padding.
  It retains all eight ribs. No rib is removed or replaced. The monochromes
  turn colored structural details into knockout space and omit recess shading;
  they are geometric one-color artwork, not desaturated color images.

The small-size limitations and glyph differences need maintainer judgment.
Passing tests or automated review does not authorize changing the approved
identity or establish visual acceptance.

## Typeface and licensing

Montserrat is by Julieta Ulanovsky, Sol Matas, Juan Pablo del Peral and
Jacques Le Bailly, distributed under the SIL Open Font License 1.1.
The original license and copyright notice are included in `source/OFL.txt`.
The unmodified variable font in `source/Montserrat-wght.ttf` is a reproducible
build input. Production SVGs contain glyph paths only: no embedded font,
`text` element, installed-font dependency or external font request.

Upstream: [Google Fonts Montserrat directory at the pinned commit](https://github.com/google/fonts/tree/8b0a1d0f5983c89bc2b93f1b5fb55f9e252744b5/ofl/montserrat).
Font SHA-256: `0f7b311b2f3279e4eef9b2f968bcdbab6e28f4daeb1f049f4f278a902bcd82f7`.
License notice SHA-256 (LF, trailing spaces removed):
`fa036c290600f85c9c7747dbd2bd3ac26d28de0ac0582ff9320c53161765e19b`.
The font license applies to the font input; ContainerBin's repository license
continues to cover its original material. No proprietary font is included.

## Reproduction and validation

From a Windows checkout, with Docker Desktop running in Linux-container mode:

```powershell
# Use the absolute path to YOUR checkout. The script resolves its own root.
& 'D:\Work\GIT\container-bin\scripts\build-branding.ps1'
& 'D:\Work\GIT\container-bin\scripts\build-branding.ps1' -Check
```

The wrapper uses digest-pinned Python and Node images, `fonttools==4.60.1`,
and `sharp==0.35.5` with an npm integrity lockfile. Packages live only inside
the disposable containers. No global workstation runtime, runtime dependency,
font installation, Go module change, CI change or release-workflow change is
needed. Network access is needed for the pinned images/packages on a fresh
machine. Image digests are in the wrapper, not mutable tags.

`scripts/build-branding.py` reads the icon master and licensed font to produce
the five other SVGs. It validates XML, permits only native shapes/gradients,
rejects resource references except local gradient IDs, counts ribs and glyphs,
and compares light/dark geometry. `scripts/export-branding.cjs` checks source
hashes, renders every SVG, validates PNG dimensions/alpha and transparent canvas
borders, and builds the proof sheets. Preview captions use temporary licensed
glyph outlines too, so the build works in a fontless container. Their ignored
`.preview-glyphs.json` cache is regenerated from the font, not distributed as
an extra production asset.

Every export starts from its corresponding SVG, never a resized reference PNG
or another PNG export. `-Check` requires existing SVG derivatives and all raster
exports/proofs to match regeneration byte-for-byte. The pinned Linux container
is the canonical rendering environment; different sharp/libvips builds may
change raster edge pixels. Review `comparison.png` and `small-sizes.png` after
any master, font-fit, palette or tooling edit. Do not accept a clean numerical
check as a substitute for looking at the images.
