import { test } from 'node:test';
import assert from 'node:assert/strict';
import { docsLinks, SAAS_PUBLIC_BASE_URL } from './docsLinks.ts';

const EXPECTED_KEYS = [
  'installation',
  'enrollment',
  'deviceRole',
  'processingModes',
  'cameraDiscovery',
  'cameraCredentials',
  'dvrNvr',
  'commissioning',
  'troubleshooting',
] as const;

test('SAAS_PUBLIC_BASE_URL is the confirmed public URL, over HTTPS', () => {
  assert.equal(SAAS_PUBLIC_BASE_URL, 'https://vps-6387636-x.dattaweb.com');
  assert.ok(SAAS_PUBLIC_BASE_URL.startsWith('https://'));
});

test('docsLinks defines exactly the keys every wizard step needs', () => {
  assert.deepEqual(Object.keys(docsLinks).sort(), [...EXPECTED_KEYS].sort());
});

test('every docs link is non-empty, HTTPS, and derived from the public base URL', () => {
  for (const [key, url] of Object.entries(docsLinks)) {
    assert.ok(url.length > 0, `${key} is empty`);
    assert.ok(url.startsWith('https://'), `${key} is not HTTPS: ${url}`);
    assert.ok(url.startsWith(SAAS_PUBLIC_BASE_URL), `${key} is not derived from SAAS_PUBLIC_BASE_URL: ${url}`);
  }
});

test('no docs link embeds a credential, token, or code', () => {
  const secretLike = /token|password|secret|api[-_]?key|credential|code=/i;
  for (const [key, url] of Object.entries(docsLinks)) {
    assert.ok(!secretLike.test(url), `${key} looks like it embeds a secret: ${url}`);
  }
});

test('docs links do not point at github.com', () => {
  for (const [key, url] of Object.entries(docsLinks)) {
    assert.ok(!url.includes('github.com'), `${key} points at GitHub, not the SaaS: ${url}`);
  }
});
