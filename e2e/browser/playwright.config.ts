// Chromium on this host drives the real panel in the container (design
// §14.2, D12). The test names resolve to the container, and Pebble's issuing
// root is trusted through the NSS database (e2e/scripts/browser.sh). The
// recovery spec reuses the admin the setup spec creates, and the milestone
// 2 specs the one recovery leaves, so they run in that order, one at a
// time.
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
  // Milestone 1 runs setup and recovery (--project recovery). Milestone 2
  // runs the rest (--project m2-default), in order, as the admin recovery
  // left: install PHP 8.4, then the sites, their certificates and the
  // switch, then the new default, which needs the sites to exist.
  // Milestone 3 runs monitoring, php84's security and the log settings
  // (--project m3-settings), on the sites milestone 2 left.
  projects: [
    { name: 'setup', testMatch: 'setup.spec.ts' },
    { name: 'recovery', testMatch: 'recovery.spec.ts', dependencies: ['setup'] },
    { name: 'm2-install', testMatch: 'php-versions.spec.ts', grep: /done-when 3/ },
    { name: 'm2-sites', testMatch: 'sites.spec.ts', dependencies: ['m2-install'] },
    { name: 'm2-default', testMatch: 'php-versions.spec.ts', grep: /done-when 6/, dependencies: ['m2-sites'] },
    { name: 'm3-monitoring', testMatch: 'monitoring.spec.ts' },
    { name: 'm3-security', testMatch: 'security.spec.ts', dependencies: ['m3-monitoring'] },
    { name: 'm3-settings', testMatch: 'settings.spec.ts', dependencies: ['m3-security'] },
  ],
});
