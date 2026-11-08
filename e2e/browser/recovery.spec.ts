// Recovery (design §4): `sudo zimam-agent panel setup-link` prints a new
// link and invalidates the old one. With an admin, the link resets its
// email, password and 2FA, and the next sign-in must set up 2FA again.
import { expect } from '@playwright/test';
import { enrol, expectDashboard, loadAdmin, newSetupLink, password, saveAdmin, signIn, signOut, test } from './support.ts';

test('a new setup link from SSH resets access and 2FA', async ({ page }) => {
  const admin = loadAdmin();

  const first = newSetupLink();
  const second = newSetupLink();
  expect(second).not.toBe(first);
  expect((await page.goto(first))?.status()).toBe(404);

  const email = 'recovered@zimam.test';
  const pass = password();
  await page.goto(second);
  await expect(page.getByRole('heading', { name: 'Reset access' })).toBeVisible();
  await page.getByLabel('Email address').fill(email);
  await page.getByRole('textbox', { name: /^Password/ }).fill(pass);
  await page.getByRole('textbox', { name: /^Confirm password/ }).fill(pass);
  await page.getByRole('button', { name: 'Reset access' }).click();

  // The reset cleared 2FA: set-up comes first again.
  await expect(page.getByRole('button', { name: 'Set up', exact: true })).toBeVisible();
  await signOut(page);

  // The old password is gone, and the new one leads to 2FA set-up, not a code.
  await signIn(page, admin.email, admin.password);
  await expect(page.getByText('These credentials do not match our records.')).toBeVisible();
  await signIn(page, email, pass);
  await expect(page.getByRole('button', { name: 'Set up', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Verify your identity' })).toHaveCount(0);

  const { secret, recoveryCodes } = await enrol(page, pass);
  await expectDashboard(page);
  // The milestone 2 specs sign in as this admin.
  saveAdmin({ email, password: pass, secret, recoveryCodes });
});
