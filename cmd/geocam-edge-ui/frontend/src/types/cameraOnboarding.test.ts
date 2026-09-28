import { test } from 'node:test';
import assert from 'node:assert/strict';
import { candidateSelectable } from '../utils/cameraOnboardingDisplay.ts';

// services/api.ts's camera-onboarding wrappers are not imported here for the
// same reason src/types/processingMode.test.ts does not import services/api.ts:
// the generated Wails bindings are extensionless relative ES module imports,
// which plain `node --test` cannot resolve without a bundler. The business
// logic those wrappers call is covered by the Go-side
// internal/installer/camera_onboarding_test.go and camera_discovery.go tests.

test('candidateSelectable rejects a multi-source (DVR/NVR) candidate', () => {
  assert.equal(candidateSelectable({ multi_source: true, onvif_available: true }), false);
});

test('candidateSelectable rejects a candidate with no ONVIF service', () => {
  assert.equal(candidateSelectable({ multi_source: false, onvif_available: false }), false);
});

test('candidateSelectable accepts a single-source candidate with ONVIF available', () => {
  assert.equal(candidateSelectable({ multi_source: false, onvif_available: true }), true);
});
