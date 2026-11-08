// Milestone 3's logs (design §2 done-when 10, §11.3, §11.4), through the
// panel as the admin: a 201 MiB log rotates and is compressed, then the
// retention set to 1 day in Settings deletes an archive backdated 3 days,
// and nothing else. The checks from outside the browser are e2e/scenarios/
// m3-logs.sh's.
import { expect } from '@playwright/test';
import { panel, scenario, signInAsAdmin, test } from './support.ts';

test('done-when 10: rotation at 200 MB, and retention set in Settings', async ({ page }) => {
  test.setTimeout(30 * 60_000);
  await test.step('a 201 MiB log is compressed by maintenance', async () => {
    scenario('m3-logs.sh', 'rotate');
  });

  await test.step('retention set to 1 day in Settings deletes only the backdated archive', async () => {
    await signInAsAdmin(page);
    await page.goto(`${panel}/admin/settings`);
    const days = page.locator('input[id$="log_retention_days"]');
    await expect(days).toHaveValue('14');
    await days.fill('1');
    scenario('m3-logs.sh', 'mark');
    await page.getByRole('button', { name: 'Save settings' }).click();
    await expect(page.getByText('Applying the log settings')).toBeVisible();
    scenario('m3-logs.sh', 'policy');
    await page.reload();
    await expect(page.locator('input[id$="log_retention_days"]')).toHaveValue('1');
    scenario('m3-logs.sh', 'retention');
  });
});
