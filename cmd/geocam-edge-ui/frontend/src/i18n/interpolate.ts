// Pure `{{var}}` template interpolation, factored out of I18nContext so it
// can be unit-tested with plain node:test (no jsdom/React renderer in this
// repo's test setup -- see src/types/cameraOnboarding.test.ts).
export function interpolate(template: string, vars?: Record<string, string | number>): string {
  if (!vars) return template;
  return Object.entries(vars).reduce(
    (acc, [name, value]) => acc.split(`{{${name}}}`).join(String(value)),
    template,
  );
}
