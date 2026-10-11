"""Rebuild native SVG derivatives; fontTools is isolated build-time tooling only."""

import copy
import hashlib
import json
import sys
from pathlib import Path
import xml.etree.ElementTree as ET

import fontTools
from fontTools.pens.boundsPen import BoundsPen
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont


ROOT = Path(__file__).resolve().parents[1] / "assets" / "branding"
NS = "http://www.w3.org/2000/svg"
ET.register_namespace("", NS)


def tag(name):
    return f"{{{NS}}}{name}"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def write(name, root):
    ET.indent(root, space="  ")
    data = (ET.tostring(root, encoding="unicode") + "\n").encode("utf-8")
    if "--check" in sys.argv:
        require((ROOT / name).read_bytes() == data, f"{name} is stale")
    else:
        (ROOT / name).write_bytes(data)


def svg(title, viewbox="150 180 960 960"):
    root = ET.Element(tag("svg"), {"viewBox": viewbox, "role": "img", "aria-labelledby": "title"})
    ET.SubElement(root, tag("title"), {"id": "title"}).text = title
    return root


def main():
    if fontTools.__version__ != "4.60.1":
        raise RuntimeError("Use fonttools==4.60.1 for stable outlines")
    font_path = ROOT / "source" / "Montserrat-wght.ttf"
    if hashlib.sha256(font_path.read_bytes()).hexdigest() != "0f7b311b2f3279e4eef9b2f968bcdbab6e28f4daeb1f049f4f278a902bcd82f7":
        raise RuntimeError("Unexpected Montserrat source hash")
    master = ET.parse(ROOT / "icon.svg").getroot()
    # The master owns every icon coordinate. Derivatives never retrace the raster.
    art = [e for e in master if e.tag not in (tag("title"), tag("desc"), tag("defs"))]
    for name, color in (("icon-monochrome.svg", "#131d28"), ("icon-monochrome-white.svg", "#ffffff")):
        root = svg("ContainerBin monochrome")
        # Cyan features become true negative space. The recess shading is omitted.
        paths = [p.attrib["d"] for e in art if e.attrib.get("id") != "terminal-recess"
                 for p in e.iter(tag("path"))]
        ET.SubElement(root, tag("path"), {"fill": color, "fill-rule": "evenodd", "d": " ".join(paths)})
        write(name, root)
    favicon = copy.deepcopy(master)
    favicon.set("viewBox", "170 210 910 910")
    # Optical adjustment only: enlarge the same prompt about (780, 760).
    favicon.find(".//*[@id='prompt']").set("transform", "translate(-140.4 -136.8) scale(1.18)")
    write("favicon.svg", favicon)

    font = instantiateVariableFont(TTFont(font_path), {"wght": 900})
    glyphs, cmap = font.getGlyphSet(), font.getBestCmap()
    # Measured solid-ink bounds in wordmark-light.png; common to BOTH themes.
    # These placements fit the licensed outlines, not generated raster edge noise.
    boxes = [(568, 273, 718, 443), (720, 317, 848, 443), (855, 318, 970, 441),
             (973, 288, 1045, 442), (1051, 317, 1167, 443), (1175, 267, 1219, 441),
             (1230, 318, 1346, 441), (1353, 317, 1480, 443), (1488, 318, 1560, 441),
             (1569, 274, 1701, 442), (1707, 267, 1752, 441), (1761, 318, 1877, 441)]
    outlines = []
    for char, (left, top, right, bottom) in zip("ContainerBin", boxes):
        glyph = glyphs[cmap[ord(char)]]
        bounds = BoundsPen(glyphs)
        glyph.draw(bounds)
        x0, y0, x1, y1 = bounds.bounds
        sx, sy = (right - left) / (x1 - x0), (bottom - top) / (y1 - y0)
        pen = SVGPathPen(glyphs, ntos=lambda value: f"{value:.3f}".rstrip("0").rstrip("."))
        glyph.draw(TransformPen(pen, (sx, 0, 0, -sy, left - x0 * sx, bottom + y0 * sy)))
        outlines.append(pen.getCommands())
    for theme, color in (("light", "#111b24"), ("dark", "#f7f9fc")):
        root = svg("ContainerBin", "0 0 2048 682")
        root.append(copy.deepcopy(master.find(tag("defs"))))
        icon = ET.SubElement(root, tag("g"), {"transform": "translate(100 85) scale(.405)"})
        icon.extend(copy.deepcopy(art))
        for word, indices, fill in (("Container", range(9), color), ("Bin", range(9, 12), "#01b7fd")):
            group = ET.SubElement(root, tag("g"), {"id": word, "fill": fill})
            for i in indices:
                ET.SubElement(group, tag("path"), {"d": outlines[i]})
        write(f"logo-{theme}.svg", root)
    # Preview labels also use outlines, so a fontless Docker image produces the
    # same comparison sheets as Windows. This temporary cache is not an asset.
    label_font = instantiateVariableFont(TTFont(font_path), {"wght": 600})
    label_glyphs, label_cmap = label_font.getGlyphSet(), label_font.getBestCmap()
    labels = {}
    for code in range(32, 127):
        name = label_cmap[code]
        pen = SVGPathPen(label_glyphs, ntos=lambda v: f"{v:.3f}".rstrip("0").rstrip("."))
        label_glyphs[name].draw(pen)
        labels[chr(code)] = {"d": pen.getCommands(), "advance": label_font["hmtx"][name][0]}
    (ROOT / "source" / ".preview-glyphs.json").write_text(json.dumps(labels, sort_keys=True) + "\n", encoding="utf-8")
    validate()


def validate():
    allowed = {tag(n) for n in ("svg", "title", "desc", "defs", "linearGradient", "stop", "g", "path")}
    for file in ROOT.glob("*.svg"):
        root = ET.parse(file).getroot()
        require(root.tag == tag("svg"), f"{file}: invalid SVG root")
        ids = {e.attrib["id"] for e in root.iter() if "id" in e.attrib}
        for e in root.iter():
            require(e.tag in allowed, f"{file}: unexpected element {e.tag}")
            for key, value in e.attrib.items():
                require(key not in ("href", "style") and not key.startswith("on"), f"{file}: unexpected attribute {key}")
                if "url(" in value:
                    require(value.startswith("url(#") and value.endswith(")"), f"{file}: external resource {value}")
                    require(value[5:-1] in ids, f"{file}: missing resource {value}")
        require(len(list(root.iter(tag("path")))) <= 30, f"{file}: excessive path count")
    light = ET.parse(ROOT / "logo-light.svg").getroot()
    dark = ET.parse(ROOT / "logo-dark.svg").getroot()
    dark.find(f".//*[@id='Container']").set("fill", "#111b24")
    require(ET.tostring(light) == ET.tostring(dark), "Theme geometry differs")
    for group in ("top-ribs", "side-ribs"):
        require(len(list(ET.parse(ROOT / "icon.svg").find(f".//*[@id='{group}']"))) == 4, f"{group}: expected four ribs")
    require(len(light.find(".//*[@id='Container']")) == 9, "Expected nine Container glyphs")
    require(len(light.find(".//*[@id='Bin']")) == 3, "Expected three Bin glyphs")
    print("Validated six native SVGs, four + four ribs, 12 glyphs, identical theme geometry")


if __name__ == "__main__":
    main()
