// Milestone 2's sites (design §2, §9, §10), through the panel as the admin:
// done-when 4 creates a static site and two PHP sites on different
// versions, done-when 7 issues their certificates from Pebble, and
// done-when 5 switches php84 to PHP 8.5 and back while a request loop runs
// over HTTPS. The checks from outside the browser are the scenarios' (e2e/
// scenarios/m2-*.sh). The sites stay, for milestone 3.
import { expect, type Page, test } from '@playwright/test';
import { panel, scenario, signInAsAdmin, untilText } from './support.ts';

const minutes = 60_000;

// create fills the create form and waits for the site to be active, and
// returns its page's URL.
async function create(page: Page, name: string, php: string | null): Promise<string> {
  await page.goto(`${panel}/admin/sites/create`);
  await page.getByRole('textbox', { name: /^Name/ }).fill(name);
  await page.getByRole('textbox', { name: /^Domain/ }).fill(`${name}.zimam.test`);
  if (php === null) {
    await page.locator('label[for="form.type-static"]').click();
  } else {
    await page.locator('label[for="form.type-php"]').click();
    await page.getByRole('combobox', { name: /^PHP version/ }).selectOption(php);
  }
  await page.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/sites\/\d+$/);
  const url = page.url();
  await untilText(page, /Status\s+Active/, /Status\s+Failed/, 5 * minutes);
  return url;
}

async function issueCertificate(page: Page, url: string): Promise<void> {
  await page.goto(url);
  await page.getByRole('button', { name: 'Issue certificate' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Issue certificate' }).click();
  await untilText(page, /Certificate\s+Issued, expires \d{1,2} [A-Z][a-z]{2} \d{4}/, /Certificate error/, 5 * minutes);
}

async function switchTo(page: Page, url: string, version: string): Promise<void> {
  await page.goto(url);
  await page.getByRole('button', { name: 'Switch PHP version' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('combobox', { name: /^PHP version/ }).selectOption(version);
  await dialog.getByRole('button', { name: 'Switch', exact: true }).click();
  await expect(page.getByText(`Switching php84 to PHP ${version}`)).toBeVisible();
  await untilText(page, new RegExp(`PHP version\\s+${version.replace('.', '\\.')}\\b`), /Last error/, 5 * minutes);
}

test('done-when 4, 7 and 5: three sites, their certificates, and a switch without a failed request', async ({ page }) => {
  test.setTimeout(45 * minutes);
  await signInAsAdmin(page);

  const urls: Record<string, string> = {};
  await test.step('done-when 4: a static site and PHP sites on 8.4 and 8.5', async () => {
    urls.static = await create(page, 'static', null);
    urls.php84 = await create(page, 'php84', '8.4');
    urls.php85 = await create(page, 'php85', '8.5');
    scenario('m2-sites.sh');
  });

  await test.step('done-when 7: certificates from Pebble, and HTTPS everywhere', async () => {
    for (const name of ['static', 'php84', 'php85']) {
      await issueCertificate(page, urls[name]);
    }
    scenario('m2-certs.sh');
  });

  await test.step('done-when 5: php84 to 8.5 and back while requests run', async () => {
    scenario('m2-switch.sh', 'start');
    try {
      await switchTo(page, urls.php84, '8.5');
      await switchTo(page, urls.php84, '8.4');
    } finally {
      scenario('m2-switch.sh', 'stop');
    }
  });
});
