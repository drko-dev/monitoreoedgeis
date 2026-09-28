import { test } from 'node:test';
import assert from 'node:assert/strict';
import { statSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const cmdRoot = resolve(here, '../../..'); // frontend/src/assets -> cmd/geocam-edge-ui

// Guards against ever shipping the Wails placeholder icon again, or an
// asset file that silently went missing/empty. A real GEO CAM asset
// derived from the official source image is always well above `minBytes`;
// a blank/placeholder file is not. A legitimate small icon (16x16, 32x32)
// genuinely compresses to a few hundred bytes, so the floor is per-file,
// not one constant for every resolution.
function assertRealAsset(relativePath: string, minBytes: number) {
  const full = resolve(cmdRoot, relativePath);
  const size = statSync(full).size;
  assert.ok(size > minBytes, `${relativePath} is only ${size} bytes -- looks like a placeholder`);
}

test('header/App isotype asset exists and is not a placeholder', () => {
  assertRealAsset('frontend/src/assets/geocam-isotype.png', 10_000);
});

test('full logo lockup asset exists and is not a placeholder', () => {
  assertRealAsset('frontend/src/assets/geocam-logo-full.png', 10_000);
});

test('macOS app icon source (build/appicon.png, used by `wails build` for iconfile.icns) is not a placeholder', () => {
  assertRealAsset('build/appicon.png', 10_000);
});

test('Windows .ico exists and is not a placeholder', () => {
  assertRealAsset('build/windows/icon.ico', 10_000);
});

test('Linux icon set exists at every standard resolution', () => {
  // A real 16x16 legitimately compresses to a few hundred bytes -- the
  // floor only needs to catch a zero-byte/corrupt/placeholder file, not
  // match the byte count of the larger master assets above.
  for (const size of [16, 32, 48, 64, 128, 256, 512]) {
    assertRealAsset(`build/linux/icons/geocam-edge-ui-${size}.png`, 300);
  }
});
