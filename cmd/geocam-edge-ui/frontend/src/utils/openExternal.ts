import { BrowserOpenURL } from '../../wailsjs/runtime/runtime';

// Every docs link must open in the OS's default browser, never inside the
// Wails WebView (see docs/runbooks/EDGE_INSTALLER_UI_UX.md). BrowserOpenURL
// is the official Wails v2 runtime API for this on macOS/Windows/Linux
// alike -- one call site, no per-platform branching needed here.
//
// Outside a packaged Wails binary (e.g. `vite dev` in a plain browser tab)
// `window.runtime` doesn't exist; fall back to a normal new-tab open so the
// link still works during frontend-only development.
export function openExternal(url: string): void {
  if (typeof window !== 'undefined' && (window as unknown as { runtime?: unknown }).runtime) {
    BrowserOpenURL(url);
    return;
  }
  window.open(url, '_blank', 'noopener,noreferrer');
}
