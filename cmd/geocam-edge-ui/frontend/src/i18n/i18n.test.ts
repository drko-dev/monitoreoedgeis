import { test } from 'node:test';
import assert from 'node:assert/strict';
import { es, en, dictionaries } from './translations.ts';
import { interpolate } from './interpolate.ts';

test('default language is es', () => {
  assert.equal(dictionaries.es, es);
});

test('es and en dictionaries define exactly the same keys', () => {
  const esKeys = Object.keys(es).sort();
  const enKeys = Object.keys(en).sort();
  assert.deepEqual(enKeys, esKeys);
});

test('every translation value is non-empty', () => {
  for (const [key, value] of Object.entries(es)) {
    assert.ok(value.length > 0, `es.${key} is empty`);
  }
  for (const [key, value] of Object.entries(en)) {
    assert.ok(value.length > 0, `en.${key} is empty`);
  }
});

// es -> en / en -> es: same key must resolve to a language-appropriate,
// distinct string for every screen area touched by this pass.
test('es -> en switch changes the rendered text for each screen area', () => {
  const sampleKeys: (keyof typeof es)[] = [
    'enrollment.title',
    'mode.chooseTitle',
    'camera.discoveryTitle',
    'camera.credentialsTitle',
    'state.title',
    'app.addCamera',
  ];
  for (const key of sampleKeys) {
    assert.notEqual(es[key], en[key], `${key} is identical in es and en`);
  }
});

test('en -> es switch round-trips back to the same es dictionary', () => {
  assert.deepEqual(dictionaries.en, en);
  assert.deepEqual(dictionaries.es, es);
});

test('interpolate substitutes a single variable', () => {
  assert.equal(interpolate('Version {{version}}', { version: 'v1.2.3' }), 'Version v1.2.3');
});

test('interpolate substitutes the same variable repeated twice', () => {
  assert.equal(interpolate('{{x}}-{{x}}', { x: 'A' }), 'A-A');
});

test('interpolate with no vars returns the template unchanged', () => {
  assert.equal(interpolate('Hola'), 'Hola');
});

test('interpolate leaves unknown placeholders untouched', () => {
  assert.equal(interpolate('{{known}} {{unknown}}', { known: 'ok' }), 'ok {{unknown}}');
});
