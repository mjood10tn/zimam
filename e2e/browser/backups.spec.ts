// v0.5.0's Backups page through the panel as the admin, against the S3 sink
// (e2e/sinks): a destination is added with its encryption keys shown once in
// the dialog and sent only after their last 6 characters are typed back, it
// becomes ready and passes its test, "Show keys again" behind the password
// shows the same keys, and it is deleted again. The page never keeps the
// keys: after the dialog closes they are gone from it. Screenshots of the
// Backups and Notifications pages at 400 px and 1280 px go to
// $E2E_WORK/screens. The backup checks from outside the browser are
// e2e/scenarios/v05-backups.sh's. LXD only, like the sinks.
import { mkdirSync, readFileSync } from 'node:fs';
import { expect, type Page } from '@playwright/test';
import { confirm, livewireCall, panel, signInAsAdmin, target, test, untilText, work } from './support.ts';

const minutes = 60_000;
const screens = `${work}/screens`;

function sinks(): Record<string, string> {
  return Object.fromEntries(
    readFileSync(`${work}/sinks/sinks.env`, 'utf8')
      .split('\n')
      .filter((l) => l.includes('='))
      .map((l) => [l.slice(0, l.indexOf('=')), l.slice(l.indexOf('=') + 1)]),
  );
}

async function shoot(page: Page, name: string, url: string): Promise<void> {
  mkdirSync(screens, { recursive: true });
  for (const width of [400, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(url);
    await page.waitForTimeout(1_000);
    await page.screenshot({ path: `${screens}/${name}-${width}.png`, fullPage: true });
  }
  await page.setViewportSize({ width: 1280, height: 720 });
}

test('v0.5.0: a backup destination with its keys shown once, tested, its keys shown again, and deleted', async ({ page }) => {
  test.skip(target !== 'lxd', 'the sinks run in LXD only');
  test.setTimeout(15 * minutes);
  const s = sinks();
  await signInAsAdmin(page);
  await page.goto(`${panel}/admin/backups`);

  const mounted = livewireCall(page, 'mountAction');
  await page.getByRole('button', { name: 'Add destination' }).click();
  await mounted;
  const dialog = page.getByRole('dialog', { name: 'Add a backup destination' });
  const password = await dialog.locator('#backup-password-new').inputValue();
  const salt = await dialog.locator('#backup-salt-new').inputValue();
  expect(password).toMatch(/^[A-Za-z0-9_-]{43}$/);
  expect(salt).toMatch(/^[A-Za-z0-9_-]{43}$/);
  await dialog.getByRole('textbox', { name: /^Name/ }).fill('panel-s3');
  await dialog.getByRole('textbox', { name: /^Endpoint/ }).fill(s.S3_ENDPOINT);
  await dialog.getByRole('textbox', { name: /^Region/ }).fill('us-east-1');
  await dialog.getByRole('textbox', { name: /^Bucket/ }).fill(s.S3_BUCKET);
  await dialog.getByRole('textbox', { name: /^Folder in the bucket/ }).fill('panel');
  await dialog.getByRole('textbox', { name: /^Access key ID/ }).fill(s.S3_ACCESS);
  await dialog.getByLabel('Secret access key', { exact: false }).first().fill(s.S3_SECRET);
  // A wrong check keeps the dialog open with the same keys.
  await dialog.getByRole('textbox', { name: /^Last 6 characters/ }).fill('xxxxxx');
  await dialog.getByRole('button', { name: 'Add destination', exact: true }).click();
  await expect(dialog.getByText('That doesn’t match the end of the password above.')).toBeVisible();
  expect(await dialog.locator('#backup-password-new').inputValue()).toBe(password);
  await dialog.getByRole('textbox', { name: /^Last 6 characters/ }).fill(password.slice(-6));
  await dialog.getByRole('button', { name: 'Add destination', exact: true }).click();
  await expect(dialog).toBeHidden();
  expect(await page.content()).not.toContain(password);

  await untilText(page, /S3-compatible\s+Ready/, /S3-compatible\s+Failed/, 5 * minutes);
  await page.getByRole('row', { name: /panel-s3/ }).getByRole('button', { name: 'Test' }).click();
  await untilText(page, /Tested /, /Last send failed|rclone/, 5 * minutes);

  await confirm(page, page.getByRole('row', { name: /panel-s3/ }).getByRole('button', { name: 'Show keys again' }), /Show the keys of panel-s3/, 'Show keys');
  await expect(page.locator('input[id^="backup-password-"]').first()).toHaveValue(password);
  await page.getByRole('button', { name: 'I have stored them' }).click();
  await expect(page.locator('input[id^="backup-password-"]')).toHaveCount(0);

  await shoot(page, 'backups', `${panel}/admin/backups`);
  await shoot(page, 'notifications', `${panel}/admin/notifications`);

  await page.goto(`${panel}/admin/backups`);
  await confirm(page, page.getByRole('row', { name: /panel-s3/ }).getByRole('button', { name: 'Delete' }), /Delete destination panel-s3/, 'Delete');
  await untilText(page, /No destination yet/, null, 5 * minutes);
});
