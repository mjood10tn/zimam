// Done-when 2 (design §2): through the installer's setup link you set your
// email and password, and after the first sign-in the panel makes you set
// up 2FA. Then signing in again asks for a code.
import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { code, enrol, expectDashboard, panel, password, saveAdmin, signIn, signOut, work } from './support.ts';

test('the setup link creates the admin, who must set up 2FA, and signing in again asks for a code', async ({ page }) => {
  const link = readFileSync(`${work}/setup-link`, 'utf8').trim();
  const email = 'admin@zimam.test';
  const pass = password();

  await page.goto(link);
  await expect(page.getByRole('heading', { name: 'Create the admin account' })).toBeVisible();
  await page.getByLabel('Email address').fill(email);
  await page.getByRole('textbox', { name: /^Password/ }).fill(pass);
  await page.getByRole('textbox', { name: /^Confirm password/ }).fill(pass);
  await page.getByRole('button', { name: 'Create account' }).click();

  // Signed in, and Filament asks for 2FA before anything else, the
  // dashboard included.
  await expect(page.getByRole('button', { name: 'Set up', exact: true })).toBeVisible();
  await page.goto(`${panel}/admin`);
  await expect(page.getByRole('button', { name: 'Set up', exact: true })).toBeVisible();

  const { secret, recoveryCodes } = await enrol(page, pass);
  saveAdmin({ email, password: pass, secret, recoveryCodes });
  await expectDashboard(page);

  await signOut(page);
  await signIn(page, email, pass);
  await expect(page.getByRole('heading', { name: 'Verify your identity' })).toBeVisible();
  await page.locator('input[autocomplete="one-time-code"]').first().click();
  await page.keyboard.type(await code(secret));
  await expectDashboard(page);

  // The link was single use. (page.goto, not page.request: only the
  // browser resolves the test names and trusts Pebble's root.)
  expect((await page.goto(link))?.status()).toBe(404);
});
