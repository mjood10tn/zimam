// Milestone 3's monitoring (design §2 done-when 8, §7.5), through the panel
// as the admin: the Monitoring page shows live values from system.metrics,
// read again every 5 seconds. The checks of the stored samples and their
// pruning are e2e/scenarios/m3-monitoring.sh's.
import { expect, test } from '@playwright/test';
import { panel, scenario, signInAsAdmin } from './support.ts';

test('done-when 8: metric samples, their pruning, and live values on the Monitoring page', async ({ page }) => {
  test.setTimeout(10 * 60_000);
  scenario('m3-monitoring.sh', 'samples');
  scenario('m3-monitoring.sh', 'prune');

  await signInAsAdmin(page);
  await page.goto(`${panel}/admin/monitoring`);
  // Blade's whitespace surrounds the texts, so the patterns aren't anchored.
  const readAt = page.getByText(/Read at \d\d:\d\d:\d\d, every 5 seconds\./);
  await expect(readAt).toBeVisible();
  const first = await readAt.innerText();
  // The CPU and network rates come from the stored sample, or from the
  // second reading.
  await expect(page.getByText(/\d+% busy/)).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText(/ available of /)).toBeVisible();
  await expect(page.getByText(/ in · .* out/)).toBeVisible();
  await expect(page.getByText(/% used, .* free of /).first()).toBeVisible();
  const services = page.getByRole('region', { name: 'Services' });
  await expect(services.locator('dt:has-text("nginx.service") + dd')).toHaveText('Active');
  // Live: the reading's time moves on without a reload.
  await expect(readAt).not.toHaveText(first, { timeout: 20_000 });
});
