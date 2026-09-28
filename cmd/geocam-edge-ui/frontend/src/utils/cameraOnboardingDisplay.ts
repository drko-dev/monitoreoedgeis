// Pure display-mapping helper, kept out of .tsx files so it can be exercised
// directly by the plain node:test runner (see src/utils/processingModeDisplay.ts
// for why: no jsdom/JSX-aware toolchain in this repo).

// A candidate is selectable only when it is single-source (DVR/NVR stays
// disabled, per UX-4 scope) and ONVIF is actually reachable on it.
export function candidateSelectable(candidate: { multi_source: boolean; onvif_available: boolean }): boolean {
  return !candidate.multi_source && candidate.onvif_available;
}
