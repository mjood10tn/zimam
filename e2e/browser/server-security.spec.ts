// v0.6.0's Server security page through the panel as the admin: the locked
// firewall rules are listed and can't be deleted; the fail2ban, Cloudflare
// and Updates sections are there; a reboot scheduled from the Updates
// section shows and is cancelled. Screenshots at 400 px and 1280 px go to
// $E2E_WORK/screens. The checks from outside the browser are
// e2e/scenarios/v06-security.sh's; the SSH form's choices are the panel's
// feature tests'.
import { mkdirSync } from 'node:fs';
import { expect } from '@playwright/test';
import { confirm, livewireCall, panel, signInAsAdmin, test, untilText, work } from './support.ts';

const minutes = 60_000;
const screens = `${work}/screens`;

test('v0.6.0: the Server security page', async ({ page }) => {
  test.setTimeout(10 * minutes);
  await signInAsAdmin(page);
  await page.goto(`${panel}/admin/server-security`);

  const firewall = page.locator('#firewall');
  await expect(firewall.getByText('ssh-22', { exact: true })).toBeVisible();
  await expect(firewall.getByText('Kept by Zimam').first()).toBeVisible();
  const row = firewall.getByRole('row').filter({ hasText: 'ssh-22' });
  await expect(row.getByRole('button', { name: 'Delete' })).toHaveCount(0);
  for (const id of ['#ssh', '#fail2ban', '#cloudflare', '#updates']) {
    await expect(page.locator(id)).toBeVisible();
  }

  // A reboot scheduled at the default 03:30, then cancelled.
  await confirm(page, page.locator('#updates').getByRole('button', { name: 'Schedule reboot' }), 'Schedule a reboot', 'Schedule reboot');
  await untilText(page, /Reboot scheduled for/, null, 3 * minutes);
  const cancelled = livewireCall(page, 'mountAction');
  await page.locator('#updates').getByRole('button', { name: 'Cancel reboot' }).click();
  await cancelled;
  await untilText(page, /No reboot is needed\.|A reboot is needed/, null, 3 * minutes);

  mkdirSync(screens, { recursive: true });
  for (const width of [400, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(`${panel}/admin/server-security`);
    await page.waitForTimeout(1_000);
    await page.screenshot({ path: `${screens}/server-security-${width}.png`, fullPage: true });
  }
});
