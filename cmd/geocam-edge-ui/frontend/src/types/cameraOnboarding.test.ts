import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  candidateSelectable,
  candidateChannelLabel,
  candidateDisplayName,
  candidateKeyForRequest,
} from '../utils/cameraOnboardingDisplay.ts';

// services/api.ts's camera-onboarding wrappers are not imported here for the
// same reason src/types/processingMode.test.ts does not import services/api.ts:
// the generated Wails bindings are extensionless relative ES module imports,
// which plain `node --test` cannot resolve without a bundler. The business
// logic those wrappers call is covered by the Go-side
// internal/installer/camera_onboarding_test.go and camera_discovery.go tests.

const singleSourceOnvif = { multi_source: false, onvif_available: true };
const singleSourceNoOnvif = { multi_source: false, onvif_available: false };
// A bare DVR/NVR device-level candidate: multi_source but no channel
// identity. DiscoverCameras never actually emits this shape (UX-6 always
// expands straight to per-channel candidates), but the UI must still refuse
// it defensively.
const bareMultiSource = { multi_source: true, onvif_available: true };
const channel1 = {
  multi_source: true,
  onvif_available: true,
  channel_label: 'CH1',
  channel_index: 1,
  channel_count: 4,
};
const channel2 = {
  multi_source: true,
  onvif_available: true,
  channel_label: 'CH2',
  channel_index: 2,
  channel_count: 4,
};
// A channel candidate missing its channel identity (backend resolution
// failed to attach index/count) -- must stay disabled, same as a bare
// device-level candidate.
const channelMissingIdentity = { multi_source: true, onvif_available: true, channel_index: undefined };

test('candidateSelectable: single-source with ONVIF is selectable', () => {
  assert.equal(candidateSelectable(singleSourceOnvif), true);
});

test('candidateSelectable: single-source without ONVIF is disabled', () => {
  assert.equal(candidateSelectable(singleSourceNoOnvif), false);
});

test('candidateSelectable: bare DVR/NVR device candidate (no channel identity) is disabled', () => {
  assert.equal(candidateSelectable(bareMultiSource), false);
});

test('candidateSelectable: DVR/NVR expanded channel candidate is selectable', () => {
  assert.equal(candidateSelectable(channel1), true);
  assert.equal(candidateSelectable(channel2), true);
});

test('candidateSelectable: channel candidate with no channel/source identity is disabled', () => {
  assert.equal(candidateSelectable(channelMissingIdentity), false);
});

test('candidateChannelLabel: undefined for single-source', () => {
  assert.equal(candidateChannelLabel(singleSourceOnvif), undefined);
});

test('candidateChannelLabel: undefined for a bare multi-source candidate', () => {
  assert.equal(candidateChannelLabel(bareMultiSource), undefined);
});

test('candidateChannelLabel: distinct labels per channel of the same device', () => {
  assert.equal(candidateChannelLabel(channel1), 'Channel 1 of 4 (CH1)');
  assert.equal(candidateChannelLabel(channel2), 'Channel 2 of 4 (CH2)');
});

test('candidateDisplayName: single-source uses model/manufacturer/host only', () => {
  assert.equal(
    candidateDisplayName({ ...singleSourceOnvif, model: 'Tapo TC70', host: '192.168.0.42' }),
    'Tapo TC70',
  );
});

test('candidateDisplayName: two channels of the same NVR never collapse to the same label', () => {
  const nvr = { model: 'NVR-8CH', host: '192.168.0.50' };
  const name1 = candidateDisplayName({ ...channel1, ...nvr });
  const name2 = candidateDisplayName({ ...channel2, ...nvr });
  assert.notEqual(name1, name2);
  assert.equal(name1, 'NVR-8CH · Channel 1 of 4 (CH1)');
  assert.equal(name2, 'NVR-8CH · Channel 2 of 4 (CH2)');
});

test('candidateKeyForRequest: echoes the exact candidate_key, per channel, unchanged', () => {
  const ch1 = { candidate_key: 'dvr-stable-id|ch=src1' };
  const ch2 = { candidate_key: 'dvr-stable-id|ch=src2' };
  assert.equal(candidateKeyForRequest(ch1), 'dvr-stable-id|ch=src1');
  assert.equal(candidateKeyForRequest(ch2), 'dvr-stable-id|ch=src2');
  assert.notEqual(candidateKeyForRequest(ch1), candidateKeyForRequest(ch2));
});
