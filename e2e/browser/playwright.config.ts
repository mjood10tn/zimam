// Chromium on this host drives the real panel in the container (design
// §14.2, D12). The test names resolve to the container, and Pebble's issuing
// root is trusted through the NSS database (e2e/scripts/browser.sh). The
// recovery spec reuses the admin the setup spec creates, so they run in
// that order, one at a time.
import { defineConfig, devices } from '@playwright/test';
import { readState, work } from './support.ts';

const { CIP } = readState();

export default defineConfig({
  testDir: '.',
  outputDir: `${work}/playwright`,
  workers: 1,
  fullyParallel: false,
  retries: 0,
  timeout: 180_000,
  expect: { timeout: 15_000 },
  reporter: [['list']],
  use: {
    ...devices['Desktop Chrome'],
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
    launchOptions: { args: [`--host-resolver-rules=MAP *.zimam.test ${CIP}`] },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'setup', testMatch: 'setup.spec.ts' },
    { name: 'recovery', testMatch: 'recovery.spec.ts', dependencies: ['setup'] },
  ],
});
