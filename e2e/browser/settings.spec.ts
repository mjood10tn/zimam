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

// R01-D19: the panel's display time zone. The panel stores UTC and shows a
// job's time three hours later with Asia/Riyadh; the zone is put back to
// UTC afterwards, also when a check fails, so later specs see UTC.
test('Settings: the display time zone moves the times the panel shows', async ({ page }) => {
  test.setTimeout(10 * 60_000);
  const stamp = /([A-Z][a-z]{2}) (\d{1,2}), (\d{4}) (\d{2}):(\d{2}):(\d{2})/;
  const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  let current = 'UTC';
  const setZone = async (zone: string) => {
    await page.goto(`${panel}/admin/settings`);
    // Filament's searchable select: a button (not a combobox) opens a
    // dropdown with a search input (role combobox) and role=option items.
    await page.getByRole('button', { name: /^Time zone/ }).click();
    await page.getByRole('combobox').fill(zone);
    await page.getByRole('option', { name: zone, exact: true }).click();
    await page.getByRole('button', { name: 'Save settings' }).click();
    if (zone !== current) await expect(page.getByText('The time zone is saved')).toBeVisible();
    current = zone;
    await page.reload();
    await expect(page.getByRole('button', { name: /^Time zone/ })).toContainText(zone);
  };
  // One succeeded job's page, picked once, so both zones read the same
  // job's Finished time: a job that finishes in between, or one still
  // running, can't take its place.
  let job = '';
  // That job's "Finished" time as the page shows it, in milliseconds.
  const finished = async (): Promise<number> => {
    if (job === '') {
      await page.goto(`${panel}/admin/agent-jobs`);
      const row = page.locator('table tbody tr').filter({ has: page.getByText('Succeeded', { exact: true }) }).first();
      await expect(row).toBeVisible();
      await row.locator('td').first().click();
      await page.waitForURL(/\/admin\/agent-jobs\/\d+$/);
      job = page.url();
    } else {
      await page.goto(job);
    }
    const entry = page.locator('.fi-in-entry').filter({ hasText: 'Finished' }).first();
    await expect(entry).toContainText(stamp);
    const m = stamp.exec((await entry.textContent()) ?? '');
    if (m === null) throw new Error('no finished time on the job page');
    return Date.UTC(Number(m[3]), months.indexOf(m[1]), Number(m[2]), Number(m[4]), Number(m[5]), Number(m[6]));
  };

  await signInAsAdmin(page);
  try {
    await setZone('UTC');
    const utc = await finished();
    await setZone('Asia/Riyadh');
    expect((await finished()) - utc).toBe(3 * 60 * 60_000);
  } finally {
    await setZone('UTC');
  }
});
