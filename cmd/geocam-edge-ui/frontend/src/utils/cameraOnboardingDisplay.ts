// Pure display-mapping helper, kept out of .tsx files so it can be exercised
// directly by the plain node:test runner (see src/utils/processingModeDisplay.ts
// for why: no jsdom/JSX-aware toolchain in this repo).

interface ChannelIdentity {
  multi_source: boolean;
  channel_label?: string;
  channel_index?: number;
  channel_count?: number;
}

// A candidate is selectable when ONVIF is reachable and, for a DVR/NVR
// (multi_source), it resolves to a concrete channel. DiscoverCameras (UX-6)
// only ever emits already-expanded per-channel candidates for a multi-source
// device -- it never emits a bare device-level candidate -- but the UI stays
// fail-closed defensively: a multi-source candidate with no channel_index is
// never selectable, since there would be no single stream to onboard.
export function candidateSelectable(candidate: {
  multi_source: boolean;
  onvif_available: boolean;
  channel_index?: number;
}): boolean {
  if (!candidate.onvif_available) return false;
  if (!candidate.multi_source) return true;
  return typeof candidate.channel_index === 'number' && candidate.channel_index >= 1;
}

// candidateChannelLabel returns a human-readable channel descriptor for a
// DVR/NVR channel candidate ("Channel 2 of 4 (CH2)"), or undefined for a
// single-source candidate or a multi-source candidate with no channel
// identity (which candidateSelectable already keeps disabled).
export function candidateChannelLabel(candidate: ChannelIdentity): string | undefined {
  if (!candidate.multi_source) return undefined;
  if (typeof candidate.channel_index !== 'number' || typeof candidate.channel_count !== 'number') {
    return undefined;
  }
  const suffix = candidate.channel_label ? ` (${candidate.channel_label})` : '';
  return `Channel ${candidate.channel_index} of ${candidate.channel_count}${suffix}`;
}

// candidateDisplayName combines the device's model/manufacturer/host with
// its channel descriptor when it has one, so two channels of the same
// DVR/NVR never look identical in a list, a credential-test header, or an
// onboarding result screen.
export function candidateDisplayName(
  candidate: ChannelIdentity & { model?: string; manufacturer?: string; host: string },
): string {
  const base = candidate.model || candidate.manufacturer || candidate.host;
  const channel = candidateChannelLabel(candidate);
  return channel ? `${base} · ${channel}` : base;
}

// candidateKeyForRequest is the single place a credential-test or onboarding
// request reads its candidate_key from. It never parses or reconstructs the
// key -- a DVR/NVR channel's composite candidate_key (UX-6) is already
// resolved server-side and only ever echoed back unchanged, exactly like a
// single-source candidate's key.
export function candidateKeyForRequest(candidate: { candidate_key: string }): string {
  return candidate.candidate_key;
}
