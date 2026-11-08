// Milestone 3's security (design §2 done-when 9, §9.6, §9.7), through the
// panel as the admin: each of php84's security settings is applied from
// its Security tab, then checked with real requests by e2e/scenarios/
// m3-security.sh; "Run checks now" runs every check; and a change that
// meets a broken Nginx config fails without touching the site. php84 ends
// with its settings as they were: HTTPS forced, nothing else on.
import { writeFileSync } from 'node:fs';
import { expect, type Page } from '@playwright/test';
import { domain, panel, password, readState, scenario, signInAsAdmin, target, untilText, work, test } from './support.ts';

const minutes = 60_000;

type Settings = {
  forceHttps: boolean;
  hsts: '' | '86400' | '2592000' | '31536000';
  headers: boolean;
  risky: boolean;
  restrict: boolean;
  allow: string;
  deny: string;
  password: boolean;
};

// The settings php84 has after its certificate: HTTPS forced, nothing else.
const baseline: Settings = { forceHttps: true, hsts: '', headers: false, risky: false, restrict: false, allow: '', deny: '', password: false };

async function setSwitch(page: Page, name: string, on: boolean): Promise<void> {
  const toggle = page.getByRole('switch', { name, exact: true });
  if ((await toggle.getAttribute('aria-checked')) !== String(on)) {
    await toggle.click();
  }
  await expect(toggle).toHaveAttribute('aria-checked', String(on));
}

// openSecurityTab opens php84's page at its Security tab, and returns the
// page's URL.
async function openSecurityTab(page: Page): Promise<string> {
  await page.goto(`${panel}/admin/sites`);
  await page.getByRole('row').filter({ hasText: `php84.${domain}` }).getByText('php84', { exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/sites\/\d+$/);
  await page.getByRole('tab', { name: 'Security' }).click();
  await expect(page.getByRole('button', { name: 'Save security settings' })).toBeEnabled();
  return page.url();
}

// apply fills php84's Security tab with s, saves it, and waits for the
// site.security job to end with outcome.
async function apply(page: Page, s: Settings, outcome: 'succeeded' | 'failed', user?: { name: string; password: string }): Promise<void> {
  await openSecurityTab(page);
  await setSwitch(page, 'Redirect HTTP to HTTPS', s.forceHttps);
  await page.getByRole('combobox', { name: /^HSTS/ }).selectOption({ value: s.hsts });
  await setSwitch(page, 'Security headers', s.headers);
  await setSwitch(page, 'Block risky PHP functions', s.risky);
  await setSwitch(page, 'Keep PHP inside the site’s folder', s.restrict);
  await page.getByRole('textbox', { name: /^Allow only these addresses/ }).fill(s.allow);
  await page.getByRole('textbox', { name: /^Block these addresses/ }).fill(s.deny);
  await setSwitch(page, 'Password protection', s.password);
  if (user) {
    // The repeater has just appeared with the switch; a click while
    // Livewire still renders it can be lost, so click until a row shows.
    const username = page.getByRole('textbox', { name: /^Username/ });
    await expect(async () => {
      if ((await username.count()) === 0) {
        await page.getByRole('button', { name: 'Add a user' }).click();
      }
      await expect(username).toBeVisible({ timeout: 3_000 });
    }).toPass({ timeout: 30_000 });
    await username.fill(user.name);
    await page.getByRole('textbox', { name: /^Password/ }).fill(user.password);
  }
  scenario('m3-security.sh', 'mark');
  await page.getByRole('button', { name: 'Save security settings' }).click();
  await expect(page.getByText('Applying security settings to php84')).toBeVisible();
  scenario('m3-security.sh', 'job', outcome);
}

test('done-when 9: every security setting applied from the panel, the checks, and a broken config', async ({ page }) => {
  test.setTimeout(30 * minutes);
  // This host as the server sees it: the LXD bridge, or the runner's
  // public address on a VPS.
  const { BRIDGE, CLIENT } = readState();
  const client = CLIENT ?? BRIDGE;
  scenario('m3-security.sh', 'setup');
  await signInAsAdmin(page);

  try {
    await test.step('HTTP served, HSTS, the header preset, risky functions and the site folder on', async () => {
      await apply(page, { ...baseline, forceHttps: false, hsts: '2592000', headers: true, risky: true, restrict: true }, 'succeeded');
      scenario('m3-security.sh', 'on');
    });

    await test.step('force_https on, everything else off again', async () => {
      await apply(page, baseline, 'succeeded');
      scenario('m3-security.sh', 'off');
    });

    await test.step('Run checks now: every check, the dotfile canary among them', async () => {
      await page.goto(`${panel}/admin/security`);
      scenario('m3-security.sh', 'mark');
      await page.getByRole('button', { name: 'Run checks now' }).click();
      await expect(page.getByText('Running the security checks')).toBeVisible();
      // The server and the three sites.
      scenario('m3-security.sh', 'job', 'succeeded', 'security.check', '4');
      await page.reload();
      await expect(page.getByText('Every check passes.')).toHaveCount(3);
      const server = page.getByRole('region', { name: 'Server' });
      await expect(server.locator('dt:has-text("Firewall active") + dd')).toContainText(target === 'vps' ? 'Pass' : 'Fail');
      await expect(server.locator('dt:has-text("Agent socket permissions") + dd')).toContainText('Pass');
      scenario('m3-security.sh', 'checks');
    });

    await test.step('the deny list', async () => {
      await apply(page, { ...baseline, deny: client }, 'succeeded');
      scenario('m3-security.sh', 'deny');
    });

    await test.step('the allow list', async () => {
      await apply(page, { ...baseline, allow: client }, 'succeeded');
      scenario('m3-security.sh', 'allow');
    });

    await test.step('password protection, with ACME challenges still answered', async () => {
      const user = { name: 'e2e', password: password() };
      writeFileSync(`${work}/basic-auth`, `${user.name}:${user.password}`, { mode: 0o600 });
      await apply(page, { ...baseline, password: true }, 'succeeded', user);
      scenario('m3-security.sh', 'password');
    });

    await test.step('a broken tls.conf: the change fails, and the site keeps its settings and answers', async () => {
      scenario('m3-security.sh', 'break');
      try {
        await apply(page, { ...baseline, password: true, headers: true }, 'failed');
        scenario('m3-security.sh', 'broken');
        const url = page.url();
        await page.goto(url);
        await untilText(page, /Last error/, null, 1 * minutes);
      } finally {
        scenario('m3-security.sh', 'restore');
      }
    });

    await test.step('back to the settings php84 started with', async () => {
      await apply(page, baseline, 'succeeded');
      scenario('m3-security.sh', 'off');
    });
  } finally {
    scenario('m3-security.sh', 'cleanup');
  }
});
