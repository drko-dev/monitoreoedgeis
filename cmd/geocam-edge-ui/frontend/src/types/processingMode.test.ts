import { test } from 'node:test';
import assert from 'node:assert/strict';
import { capabilityBadge, describeConfigKey, MODE_LABELS } from '../utils/processingModeDisplay.ts';

// services/api.ts wraps the generated Wails bindings (window.go.main.App.*)
// and is exercised in the Wails dev/build environment, not here: importing
// it under plain `node --test` would need to resolve those generated,
// extensionless bindings as real ES modules, which the existing test
// convention in this file (see installer.test.ts) also avoids. The
// business logic api.ts wraps is covered by the Go-side
// internal/installer/processing_mode_test.go suite instead.

test('capabilityBadge maps every CapabilityStatus to a distinct, non-empty badge', () => {
  const supported = capabilityBadge({ capability: 'SUPPORTED' });
  const warned = capabilityBadge({ capability: 'SUPPORTED_WITH_WARNINGS' });
  const unavailable = capabilityBadge({ capability: 'UNAVAILABLE' });
  assert.equal(supported.className, 'badge-success');
  assert.equal(warned.className, 'badge-warning');
  assert.equal(unavailable.className, 'badge-danger');
  const labels = new Set([supported.label, warned.label, unavailable.label]);
  assert.equal(labels.size, 3);
});

test('describeConfigKey gives an operator-safe label for the two managed keys', () => {
  assert.equal(describeConfigKey('GEOCAM_PROCESSING_MODE'), 'Processing engine');
  assert.equal(describeConfigKey('GEOCAM_VIDEO_PIPELINE_ENABLED'), 'Local video pipeline');
  assert.equal(describeConfigKey('GEOCAM_SOME_OTHER_KEY'), 'GEOCAM_SOME_OTHER_KEY');
});

test('MODE_LABELS covers exactly the three product modes', () => {
  assert.deepEqual(Object.keys(MODE_LABELS).sort(), ['cloud', 'full_edge', 'hybrid']);
});
