// Milestone 2's PHP versions (design §2, §8), through the panel as the
// admin: done-when 3 installs a second version, and done-when 6, after the
// sites exist, makes it the default.
import { expect, type Page } from '@playwright/test';
import { panel, scenario, signInAsAdmin, test } from './support.ts';

// row is the PHP versions table's row for a version.
function row(page: Page, version: string) {
  return page.locator('tr').filter({ has: page.getByText(version, { exact: true }) });
}

// untilRow reloads the PHP versions page until the version's row matches
// want, and fails at once if it shows Failed.
async function untilRow(page: Page, version: string, want: RegExp, timeout: number): Promise<void> {
  const end = Date.now() + timeout;
  for (;;) {
    await page.goto(`${panel}/admin/php-versions`);
    const text = (await row(page, version).count()) > 0 ? await row(page, version).innerText() : '';
    if (want.test(text)) {
      return;
    }
    if (/Failed/.test(text)) {
      throw new Error(`PHP ${version} failed: ${text}`);
    }
    if (Date.now() > end) {
      throw new Error(`PHP ${version}'s row never matched ${want}: ${text}`);
    }
    await page.waitForTimeout(5_000);
  }
}

test('done-when 3: a second PHP version installs from the panel', async ({ page }) => {
  test.setTimeout(20 * 60_000);
  await signInAsAdmin(page);
  await page.goto(`${panel}/admin/php-versions`);
  await expect(row(page, '8.5')).toContainText('Installed');

  await page.getByRole('button', { name: 'Install a version' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Version').selectOption('8.4');
  await dialog.getByRole('button', { name: 'Install', exact: true }).click();
  await expect(page.getByText('Installing PHP 8.4')).toBeVisible();

  await untilRow(page, '8.4', /Installed/, 15 * 60_000);
  scenario('m2-php.sh');
});

test('done-when 6: the default changes what new sites start with and what php runs', async ({ page }) => {
  test.setTimeout(10 * 60_000);
  await signInAsAdmin(page);
  await page.goto(`${panel}/admin/php-versions`);
  await row(page, '8.4').getByRole('button', { name: 'Make default' }).click();
  // A confirmation is an alertdialog.
  await page.getByRole('alertdialog').getByRole('button', { name: 'Confirm' }).click();
  await expect(page.getByText('Making PHP 8.4 the default')).toBeVisible();

  await untilRow(page, '8.4', /Default/, 5 * 60_000);
  scenario('m2-default.sh');

  await page.goto(`${panel}/admin/sites/create`);
  await expect(page.getByRole('combobox', { name: /^PHP version/ })).toHaveValue('8.4');
});
