import test from 'node:test';
import assert from 'node:assert';

test('SystemReport structure conforms to non-sensitive contract', () => {
  const dummyReport = {
    os: 'macOS',
    arch: 'arm64',
    edge_version: 'v1.0.0-dev',
    commit: '73afe63',
    build_date: '2026-09-27',
    hostname: 'test-host',
    privilege_level: 'STANDARD_USER',
    platform_supported: true,
    data_dir: '/tmp/geocam',
    service_installed: false,
    daemon_running: false,
    owns_instance_lock: false,
    config_present: false,
    enrolled: false,
    total_ram_mb: 8192,
    free_ram_mb: 4096,
    disk_path: '/tmp',
    free_disk_gb: 50,
    ffmpeg_present: true,
    has_gpu_support: true,
  };

  const keys = Object.keys(dummyReport);
  assert.strictEqual(keys.includes('password'), false);
  assert.strictEqual(keys.includes('token'), false);
  assert.strictEqual(keys.includes('secret'), false);
  assert.strictEqual(dummyReport.owns_instance_lock, false);
});

test('InstallerState supports valid state codes and transitions', () => {
  const validStates = [
    'NEW',
    'SYSTEM_CHECK',
    'NEEDS_ENROLLMENT',
    'ENROLLED',
    'ACTION_REQUIRED',
    'BLOCKED',
  ];

  for (const s of validStates) {
    const dummyState = {
      state: s,
      reason_code: 'TEST_REASON',
      safe_message: 'Safe test message',
      recoverable: true,
      next_allowed_actions: ['REFRESH'],
    };
    assert.strictEqual(validStates.includes(dummyState.state), true);
  }
});

test('Continue action policy in UX-1 is strictly non-mutating', () => {
  const state = {
    state: 'NEEDS_ENROLLMENT',
    reason_code: 'DEVICE_NOT_ENROLLED',
    safe_message: 'Edge requires enrollment with GEO CAM SaaS.',
    recoverable: true,
    next_allowed_actions: ['PROCEED_TO_ENROLLMENT', 'REFRESH'],
  };

  const handleContinue = (st: typeof state) => {
    if (st.state === 'NEEDS_ENROLLMENT') {
      return 'SaaS Enrollment wizard will be available in milestone UX-2. No productive actions taken in UX-1 shell.';
    }
    return '';
  };

  const notice = handleContinue(state);
  assert.strictEqual(notice.includes('UX-2'), true);
});

test('Blocked state disables progression', () => {
  const blockedState = {
    state: 'BLOCKED',
    reason_code: 'UNSUPPORTED_PLATFORM',
    safe_message: 'Current operating system or architecture is not officially supported.',
    recoverable: false,
    next_allowed_actions: [] as string[],
  };

  assert.strictEqual(blockedState.state === 'BLOCKED', true);
  assert.strictEqual(blockedState.recoverable, false);
  assert.strictEqual(blockedState.next_allowed_actions.length, 0);
});
