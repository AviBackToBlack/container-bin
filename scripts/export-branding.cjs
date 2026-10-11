/* Pinned, build-time sharp only. Run build-branding.py first. */
'use strict';
const fs = require('node:fs/promises');
const path = require('node:path');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const { createRequire } = require('node:module');
const root = path.resolve(__dirname, '../assets/branding');
const sharp = createRequire(path.join(root, 'source/package.json'))('sharp');
assert.equal(sharp.versions.sharp, '0.35.5', 'Use the pinned sharp version');
const check = process.argv.includes('--check');
const previewGlyphs = require(path.join(root, 'source/.preview-glyphs.json'));
const pngOptions = { compressionLevel: 9, adaptiveFiltering: false, palette: false };
const svg = name => path.join(root, `${name}.svg`);
const sha256 = data => crypto.createHash('sha256').update(data).digest('hex');

async function output(name, data) {
  if (check) assert.equal(sha256(await fs.readFile(path.join(root, name))), sha256(data), `${name} is stale`);
  else await fs.writeFile(path.join(root, name), data);
}

async function render(name, width) {
  return sharp(svg(name), { density: 192 }).resize({ width }).png(pngOptions).toBuffer();
}

async function validatePNG(name, data, width, height = width) {
  const metadata = await sharp(data).metadata();
  assert.equal(metadata.width, width, name);
  assert.equal(metadata.height, height, name);
  assert.equal(metadata.hasAlpha, true, name);
  const { data: pixels, info } = await sharp(data).ensureAlpha().raw().toBuffer({ resolveWithObject: true });
  let clear = 0, solid = 0, edges = 0;
  for (let i = 3; i < pixels.length; i += info.channels) {
    if (pixels[i] === 0) clear++;
    else if (pixels[i] === 255) solid++;
    else edges++;
  }
  assert(clear > 0 && solid > 0 && edges > 0, `${name}: missing transparency, opaque ink or antialiasing`);
  console.log(`${name}: ${width}x${height}, RGBA, ${clear} clear / ${solid} solid / ${edges} edge pixels`);
}

function label(text, width, color = '#233140') {
  let x = 16;
  const paths = [...text].map(char => {
    const glyph = previewGlyphs[char];
    assert(glyph, `Missing preview glyph: ${char}`);
    const result = `<path transform="translate(${x.toFixed(3)} 29) scale(.020 -.020)" d="${glyph.d}"/>`;
    x += glyph.advance * .020;
    return result;
  }).join('');
  assert(x < width, `Preview label would clip: ${text}`);
  return Buffer.from(`<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="42"><g fill="${color}">${paths}</g></svg>`);
}

async function background(width, height, kind) {
  if (kind !== 'checker') return sharp({ create: { width, height, channels: 4, background: kind } }).png().toBuffer();
  return sharp(Buffer.from(`<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}"><defs><pattern id="p" width="32" height="32" patternUnits="userSpaceOnUse"><path fill="#edf0f3" d="M0 0h32v32H0z"/><path fill="#d0d6dd" d="M0 0h16v16H0zM16 16h16v16H16z"/></pattern></defs><path fill="url(#p)" d="M0 0h${width}v${height}H0z"/></svg>`)).png().toBuffer();
}

async function comparison() {
  const width = 1800;
  const layers = [];
  layers.push({ input: label('Approved reference PNG', 880), top: 12, left: 25 });
  layers.push({ input: label('Native vector reconstruction', 880), top: 12, left: 925 });
  const refIcon = await sharp(path.join(root, 'references/primary-icon.png'))
    .extract({ left: 150, top: 180, width: 960, height: 960 }).resize(390).png().toBuffer();
  layers.push({ input: refIcon, top: 64, left: 240 });
  layers.push({ input: await render('icon', 390), top: 64, left: 1140 });
  for (const [theme, top, bg] of [['light', 490, '#ffffff'], ['dark', 840, '#0d1117']]) {
    layers.push({ input: await background(width, 350, bg), top, left: 0 });
    layers.push({ input: label(`${theme} wordmark - reference`, 850, theme === 'dark' ? '#f7f9fc' : '#233140'), top: top + 5, left: 25 });
    layers.push({ input: label(`${theme} wordmark - vector`, 850, theme === 'dark' ? '#f7f9fc' : '#233140'), top: top + 5, left: 925 });
    layers.push({ input: await sharp(path.join(root, `references/wordmark-${theme}.png`)).resize(850).png().toBuffer(), top: top + 50, left: 25 });
    layers.push({ input: await render(`logo-${theme}`, 850), top: top + 50, left: 925 });
  }
  layers.push({ input: await background(width, 420, 'checker'), top: 1190, left: 0 });
  layers.push({ input: label('Transparent artwork / light monochrome / dark monochrome', 1600), top: 1200, left: 25 });
  layers.push({ input: await render('icon', 300), top: 1250, left: 110 });
  layers.push({ input: await render('icon-monochrome', 300), top: 1250, left: 710 });
  layers.push({ input: await background(400, 350, '#0d1117'), top: 1245, left: 1300 });
  layers.push({ input: await render('icon-monochrome-white', 300), top: 1250, left: 1350 });
  layers.push({ input: label('Differences: clean opaque fills; licensed fitted glyphs; normalized shading; common icon placement. See BRANDING.md.', 1780), top: 1630, left: 10 });
  return sharp(await background(width, 1690, '#ffffff')).composite(layers).png(pngOptions).toBuffer();
}

async function smallSizes() {
  const layers = [];
  const sizes = [16, 32, 64, 256];
  for (let row = 0; row < 3; row++) {
    const top = row * 420;
    const bg = ['#ffffff', '#0d1117', 'checker'][row];
    layers.push({ input: await background(1600, 420, bg), top, left: 0 });
    for (let col = 0; col < sizes.length; col++) {
      const size = sizes[col], name = size <= 32 ? 'favicon' : 'icon';
      const ink = row === 1 ? '#f7f9fc' : '#233140';
      layers.push({ input: label(size === 256 ? '256px / actual' : `${size}px / actual and 4x nearest`, 395, ink), top: top + 8, left: col * 400 });
      const data = await render(name, size);
      layers.push({ input: data, top: top + 60, left: col * 400 + 20 });
      // The 256px proof is shown at actual size only, to keep the sheet compact.
      if (size <= 64) layers.push({ input: await sharp(data).resize(size * 4, size * 4, { kernel: 'nearest' }).png().toBuffer(), top: top + 140, left: col * 400 + 20 });
    }
  }
  return sharp(await background(1600, 1260, '#ffffff')).composite(layers).png(pngOptions).toBuffer();
}

async function main() {
  const references = {
    'primary-icon.png': '404557ace0a025a9c267fee254c23d22ffb0e828d519b2bd5828a8d8fc6fe53c',
    'wordmark-light.png': '4260ed4359422fe0f072d8d23ff135e0cc43a193bd35f509e8ab03d634518194',
    'wordmark-dark.png': '89717db9501e844a41a7d2cad88b8c4b540cfe166d27b9159ddc35e79bdb193d'
  };
  for (const [name, hash] of Object.entries(references)) {
    assert.equal(sha256(await fs.readFile(path.join(root, 'references', name))), hash, name);
  }
  for (const size of [1024, 512, 256, 64, 32]) {
    const name = `icon-${size}.png`, data = await render('icon', size);
    await validatePNG(name, data, size);
    await output(name, data);
  }
  for (const size of [16, 32]) {
    const name = `favicon-${size}.png`, data = await render('favicon', size);
    await validatePNG(name, data, size);
    await output(name, data);
  }
  for (const theme of ['light', 'dark']) {
    const name = `logo-${theme}.png`, data = await render(`logo-${theme}`, 2048);
    await validatePNG(name, data, 2048, 682);
    await output(name, data);
  }
  // Render every SVG, including both monochromes. Large-scale transparent borders
  // detect canvas clipping; all primitives must remain inside the declared viewBox.
  for (const name of ['icon', 'favicon', 'icon-monochrome', 'icon-monochrome-white', 'logo-light', 'logo-dark']) {
    const { data, info } = await sharp(await render(name, 2048)).ensureAlpha().raw().toBuffer({ resolveWithObject: true });
    for (let x = 0; x < info.width; x++) {
      assert.equal(data[(x * 4) + 3], 0, name);
      assert.equal(data[((info.height - 1) * info.width + x) * 4 + 3], 0, name);
    }
    for (let y = 0; y < info.height; y++) {
      assert.equal(data[(y * info.width) * 4 + 3], 0, name);
      assert.equal(data[(y * info.width + info.width - 1) * 4 + 3], 0, name);
    }
  }
  await output('comparison.png', await comparison());
  await output('small-sizes.png', await smallSizes());
  console.log(`Renderer: ${JSON.stringify(sharp.versions)}`);
  console.log(check ? 'All checked-in exports match byte-for-byte' : 'Exports and comparison sheets generated');
}
main().catch(error => { console.error(error); process.exitCode = 1; });
