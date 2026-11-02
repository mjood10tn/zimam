// What the browser specs share: the run's work directory, the admin the
// setup spec creates, TOTP codes, and the panel's sign-in, sign-out and 2FA
// enrolment. The selectors are Filament's English strings (Task 3 spike,
// step 6) and Zimam's own, from lang/en/zimam.php.
import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { expect, type Page } from '@playwright/test';
import { generate } from 'otplib';

export const work = process.env.E2E_WORK ?? '/tmp/zimam-e2e';
export const panel = 'https://panel.zimam.test';

export function readState(): Record<string, string> {
  const text = readFileSync(`${work}/state.env`, 'utf8');
  return Object.fromEntries(text.trim().split('\n').map((line) => line.split('=', 2) as [string, string]));
}

export type Admin = { email: string; password: string; secret: string; recoveryCodes: string[] };

export function saveAdmin(admin: Admin): void {
  writeFileSync(`${work}/admin.json`, JSON.stringify(admin), { mode: 0o600 });
}

export function loadAdmin(): Admin {
  return JSON.parse(readFileSync(`${work}/admin.json`, 'utf8'));
}

// newSetupLink runs `zimam-agent panel setup-link` in the container, as root
// over SSH would, and returns the link: its first line.
export function newSetupLink(): string {
  const asSh = fileURLToPath(new URL('../scripts/as.sh', import.meta.url));
  const out = execFileSync('bash', [asSh, 'root', 'zimam-agent', 'panel', 'setup-link'], { encoding: 'utf8' });
  const link = out.split('\n')[0];
  expect(link).toMatch(/^https:\/\/panel\.zimam\.test\/setup\/[A-Za-z0-9_-]{43}$/);
  expect(out).toContain('Panel IP allowlist: ');
  return link;
}

// code waits for a 30-second period no earlier code came from, so the panel
// never sees one code twice, then returns that period's code.
let lastPeriod = 0;
export async function code(secret: string): Promise<string> {
  let period = Math.floor(Date.now() / 30_000);
  if (period <= lastPeriod) {
    await new Promise((resolve) => setTimeout(resolve, (lastPeriod + 1) * 30_000 - Date.now() + 1_000));
    period = Math.floor(Date.now() / 30_000);
  }
  lastPeriod = period;
  return generate({ secret, epoch: period * 30 });
}

// enrol sets up the authenticator app on the page Filament forces after
// sign-in, and keeps the secret and the recovery codes.
export async function enrol(page: Page, password: string): Promise<{ secret: string; recoveryCodes: string[] }> {
  await page.getByRole('button', { name: 'Set up', exact: true }).click();
  // Filament's dialog element draws no box itself, so wait for its content.
  const dialog = page.getByRole('dialog', { name: 'Set up authenticator app' });
  await expect(dialog.getByText('Or enter this code manually:')).toBeVisible();
  const secret = (await dialog.innerText()).match(/Or enter this code manually:\s*([A-Z2-7]{16,})/)?.[1] ?? '';
  expect(secret).not.toBe('');
  await dialog.locator('input[autocomplete="one-time-code"]').first().click();
  await page.keyboard.type(await code(secret));
  await dialog.getByLabel('Current password').fill(password);
  await dialog.getByRole('button', { name: 'Next' }).click();
  const enable = dialog.getByRole('button', { name: 'Enable authenticator app' });
  await expect(enable).toBeVisible();
  const recoveryCodes = (await dialog.innerText()).match(/\b[A-Za-z0-9]{10}-[A-Za-z0-9]{10}\b/g) ?? [];
  expect(recoveryCodes).toHaveLength(8);
  await enable.click();
  await expect(page.getByText('Authenticator app has been enabled')).toBeVisible();
  await page.getByRole('button', { name: 'Continue' }).click();
  return { secret, recoveryCodes };
}

export async function signIn(page: Page, email: string, password: string): Promise<void> {
  await page.goto(`${panel}/admin/login`);
  await page.getByLabel('Email address').fill(email);
  await page.getByRole('textbox', { name: /^Password/ }).fill(password);
  await page.getByRole('button', { name: 'Sign in' }).click();
}

export async function signOut(page: Page): Promise<void> {
  await page.getByRole('button', { name: 'User menu' }).click();
  await page.getByRole('menuitem', { name: 'Sign out' }).click();
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
}

// expectDashboard checks the panel's dashboard is showing: its URL, and the
// bulletin's headline, the page's one <h1>.
export async function expectDashboard(page: Page): Promise<void> {
  await expect(page).toHaveURL(`${panel}/admin`);
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
}

export function password(): string {
  return `e2e-${Math.random().toString(36).slice(2)}-${Date.now()}`;
}
