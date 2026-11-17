// v0.3.0's databases and cache users (done-when: MySQL and Valkey from the
// panel, a database and a user for each of two Laravel sites, the new
// passwords written to .env, the fixture app using them, a queue job through
// Valkey, a rotation that closes the old connections, and deleting a site
// with its databases), through the panel as the admin. The sites "alpha" and
// "beta" deploy zimam-e2e/laravel-app from the run's Git host. The one-time
// password is checked in a real browser: it shows only after the job
// succeeded and is gone with the dialog. The checks from outside the browser
// are e2e/scenarios/v03-databases.sh's.
import { chmodSync, mkdirSync, renameSync, writeFileSync } from 'node:fs';
import { expect, type Locator, type Page } from '@playwright/test';
import { confirm, domain, openTab, panel, scenario, signInAsAdmin, untilText, work, test } from './support.ts';

const minutes = 60_000;
const script = 'v03-databases.sh';
// Site names have at least two characters.
const alpha = 'alpha';
const beta = 'beta';

// keep saves a password the panel showed, where the scenario's steps read
// it. A rotation keeps the one it replaced as ".old".
function keep(site: string, service: 'mysql' | 'valkey', password: string, rotated: boolean): void {
  const dir = `${work}/v03`;
  mkdirSync(dir, { recursive: true });
  const path = `${dir}/${site}-${service}.pw`;
  if (rotated) {
    renameSync(path, `${path}.old`);
  }
  writeFileSync(path, `${password}\n`, { mode: 0o600 });
  chmodSync(path, 0o600);
}

async function createSite(page: Page, name: string): Promise<string> {
  await page.goto(`${panel}/admin/sites/create`);
  await page.getByRole('textbox', { name: /^Name/ }).fill(name);
  await page.getByRole('textbox', { name: /^Domain/ }).fill(`${name}.${domain}`);
  await page.locator('label[for="form.type-laravel"]').click();
  await page.getByRole('combobox', { name: /^PHP version/ }).selectOption('8.4');
  await page.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/sites\/\d+$/);
  const url = page.url();
  await untilText(page, /Status\s+Active/, /Status\s+Failed/, 5 * minutes);
  return url;
}

// install runs a header action that installs a service, waits for its job,
// and checks the page then offers what an installed service offers.
async function install(page: Page, path: string, button: string, command: string, installed: Locator): Promise<void> {
  await page.goto(`${panel}/admin/${path}`);
  scenario(script, 'mark');
  await page.getByRole('button', { name: button, exact: true }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Install', exact: true }).click();
  await expect(page.getByText(button.replace('Install ', 'Installing '), { exact: true }).first()).toBeVisible();
  scenario(script, 'wait', command, '1', 'succeeded');
  await page.reload();
  await expect(installed).toBeVisible();
}

async function createDatabase(page: Page, site: string, suffix: string): Promise<void> {
  await page.goto(`${panel}/admin/databases`);
  await page.getByRole('button', { name: 'Create a database', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Create a database' });
  await dialog.getByRole('combobox', { name: /^Site/ }).selectOption(site);
  // The prefix follows the chosen site: the form has answered the choice.
  await expect(dialog.getByText(`s_${site}_`, { exact: true })).toBeVisible();
  await dialog.getByRole('textbox', { name: /^Name/ }).fill(suffix);
  await expect(dialog.getByRole('textbox', { name: /^Name/ })).toHaveValue(suffix);
  await dialog.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(page.getByText(`Creating s_${site}_${suffix}`)).toBeVisible();
}

// setPassword makes the site's user a new password on one of its tabs, waits
// for the one-time block, and returns the password shown. With writeEnv it
// then writes it to .env and waits for that job.
async function setPassword(page: Page, url: string, tab: 'Database' | 'Cache', site: string, user: string, writeEnv: boolean): Promise<string> {
  await openTab(page, url, tab);
  const trigger = page.getByRole('button', { name: /^(Set|Rotate) password$/ });
  const command = tab === 'Database' ? 'db.user' : 'cache.user';
  scenario(script, 'mark');
  await confirm(page, trigger, `Make a new password for ${user}?`, 'Make a new password');
  // The password is on the page, hidden, until the job has succeeded.
  const shown = page.locator('input[id^="service-password-"]');
  await expect(shown).toBeHidden();
  scenario(script, 'wait', command, '1', 'succeeded');
  await expect(shown).toBeVisible({ timeout: 3 * minutes });
  const password = await shown.inputValue();
  expect(password).toMatch(/^[A-Za-z0-9_-]{43}$/);
  await expect(page.getByText('The new password is active.')).toBeVisible();
  // The page's own request that follows (its poll) leaves the password in place.
  await page.waitForTimeout(4_000);
  await expect(shown).toHaveValue(password);
  if (writeEnv) {
    scenario(script, 'mark');
    await page.getByRole('button', { name: 'Write to .env', exact: true }).click();
    await expect(page.getByText('The .env is being written. The job runs in the background.')).toBeVisible();
    scenario(script, 'wait', 'site.env.write', '1', 'succeeded');
  }
  await page.getByRole('button', { name: 'I have saved it', exact: true }).click();
  await expect(shown).toHaveCount(0);
  // Once closed, nothing of it is left on the page.
  expect(await page.content()).not.toContain(password);
  return password;
}

// setEnvChoices adds what the site itself chooses: its cache and its queue
// are Valkey's. Zimam's .env write leaves CACHE_STORE and QUEUE_CONNECTION
// alone.
async function setEnvChoices(page: Page, url: string, site: string): Promise<void> {
  await openTab(page, url, 'Environment');
  await confirm(page, page.getByRole('button', { name: 'Show and edit .env', exact: true }), 'Show the .env', 'Show and edit .env');
  const editor = page.getByRole('textbox', { name: 'Contents' });
  await expect(editor).toHaveValue(/^DB_PASSWORD=[A-Za-z0-9_-]{43}$/m);
  const env = (await editor.inputValue()).replace(/^(CACHE_STORE|QUEUE_CONNECTION)=.*\n/gm, '').replace(/\n*$/, '');
  scenario(script, 'mark');
  await editor.fill(`${env}\nCACHE_STORE=redis\nQUEUE_CONNECTION=redis\n`);
  await page.getByRole('button', { name: 'Save .env', exact: true }).click();
  await expect(page.getByText(`Saving ${site}’s .env`)).toBeVisible();
  scenario(script, 'wait', 'site.env.write', '1', 'succeeded');
}

// deploy connects the site to the Git host's repository on its first call,
// and deploys.
async function deploy(page: Page, url: string, site: string, first: boolean): Promise<void> {
  if (first) {
    await openTab(page, url, 'Deployments');
    await page.getByRole('textbox', { name: /^GitHub repository/ }).fill('zimam-e2e/laravel-app');
    await page.getByRole('textbox', { name: /^Branch/ }).fill('main');
    await page.getByRole('button', { name: 'Connect repository', exact: true }).click();
    await expect(page.locator('pre').filter({ hasText: /^ssh-ed25519 [A-Za-z0-9+/]+=*$/ })).toBeVisible({ timeout: 10 * minutes });
    scenario(script, 'authorize', site);
  }
  await openTab(page, url, 'Deployments');
  scenario(script, 'mark');
  await page.getByRole('button', { name: 'Deploy now', exact: true }).click();
  await expect(page.getByText(`Deploying ${site}`).first()).toBeVisible();
  scenario(script, 'wait', 'site.deploy', '1', 'succeeded');
  scenario(script, 'serves', site);
}

// deleteSite deletes the site from its page, with the box that also deletes
// its databases ticked or not.
async function deleteSite(page: Page, url: string, site: string, withDatabases: boolean): Promise<void> {
  await page.goto(url);
  const tick = async (dialog: Locator) => {
    const box = dialog.getByRole('checkbox', { name: 'Also delete its databases' });
    if (withDatabases && !(await box.isChecked())) {
      await box.check();
    }
  };
  await confirm(page, page.getByRole('button', { name: 'Delete', exact: true }), `Delete ${site}`, 'Delete', 'mountAction', tick);
}

test('v0.3.0: MySQL and Valkey from the panel, two sites with their own users, a rotation, and deleting a site with its databases', async ({ page }) => {
  test.setTimeout(90 * minutes);
  await signInAsAdmin(page);
  scenario(script, 'snapshot', 'before');
  scenario(script, 'mem', 'before MySQL and Valkey');

  const urls: Record<string, string> = {};
  await test.step('two Laravel sites, a and b', async () => {
    urls.alpha = await createSite(page, alpha);
    urls.beta = await createSite(page, beta);
  });

  await test.step('install MySQL, then Valkey, from the panel', async () => {
    await install(page, 'databases', 'Install MySQL', 'mysql.install', page.getByRole('button', { name: 'Create a database', exact: true }));
    scenario(script, 'mem', 'MySQL installed');
    await install(page, 'cache', 'Install Valkey', 'valkey.install', page.getByText(/Valkey [0-9.]+ is installed\./));
    scenario(script, 'mem', 'Valkey installed');
    scenario(script, 'installed');
  });

  await test.step('a database for each site, named with the site’s prefix', async () => {
    scenario(script, 'mark');
    await createDatabase(page, alpha, 'app');
    await createDatabase(page, beta, 'app');
    scenario(script, 'wait', 'db.create', '2', 'succeeded');
    await page.goto(`${panel}/admin/databases`);
    for (const name of ['s_alpha_app', 's_beta_app']) {
      await expect(async () => {
        await page.reload();
        await expect(page.getByRole('row').filter({ hasText: name }).getByText('Active')).toBeVisible({ timeout: 5_000 });
      }).toPass({ timeout: 2 * minutes });
    }
  });

  await test.step('each site’s MySQL and Valkey users, shown once, written to .env', async () => {
    for (const site of [alpha, beta]) {
      keep(site, 'mysql', await setPassword(page, urls[site], 'Database', site, `s_${site}`, true), false);
      keep(site, 'valkey', await setPassword(page, urls[site], 'Cache', site, `s_${site}`, true), false);
      await setEnvChoices(page, urls[site], site);
      scenario(script, 'env', site);
      scenario(script, 'logins', site, 'new');
    }
  });

  await test.step('a second database for a, deleted again from the MySQL page, with its grants', async () => {
    scenario(script, 'mark');
    await createDatabase(page, alpha, 'scratch');
    scenario(script, 'wait', 'db.create', '1', 'succeeded');
    scenario(script, 'scratch', alpha, 'present');
    const row = page.getByRole('row').filter({ hasText: 's_alpha_scratch' });
    await expect(async () => {
      await page.reload();
      await expect(row.getByText('Active')).toBeVisible({ timeout: 5_000 });
    }).toPass({ timeout: 2 * minutes });
    scenario(script, 'mark');
    await confirm(page, row.getByRole('button', { name: 'Delete', exact: true }), 'Delete s_alpha_scratch?', 'Delete database');
    await expect(page.getByText('Deleting s_alpha_scratch')).toBeVisible();
    scenario(script, 'wait', 'db.drop', '1', 'succeeded');
    scenario(script, 'scratch', alpha, 'gone');
    await expect(async () => {
      await page.reload();
      await expect(page.getByRole('row').filter({ hasText: 's_alpha_scratch' })).toHaveCount(0, { timeout: 5_000 });
    }).toPass({ timeout: 2 * minutes });
  });

  await test.step('both sites deploy the fixture app, and use their own row, key and queue', async () => {
    await deploy(page, urls.alpha, alpha, true);
    await deploy(page, urls.beta, beta, true);
    scenario(script, 'mem', 'two Laravel sites deployed');
    scenario(script, 'worker', alpha, 'start');
    scenario(script, 'uses', alpha, `alpha-${Date.now()}`);
    scenario(script, 'uses', beta, `beta-${Date.now()}`);
    scenario(script, 'mem', 'both sites in use, a worker running');
  });

  await test.step('rotating a’s passwords closes its connections; a redeploy brings the new ones', async () => {
    keep(alpha, 'mysql', await setPassword(page, urls.alpha, 'Database', alpha, 's_alpha', true), true);
    keep(alpha, 'valkey', await setPassword(page, urls.alpha, 'Cache', alpha, 's_alpha', true), true);
    scenario(script, 'logins', alpha, 'new');
    scenario(script, 'worker', alpha, 'dead');
    scenario(script, 'env', alpha);
    // The cached config still names the old passwords.
    scenario(script, 'stale', alpha);
    await deploy(page, urls.alpha, alpha, false);
    scenario(script, 'worker', alpha, 'start');
    scenario(script, 'uses', alpha, `alpha-rotated-${Date.now()}`);
    scenario(script, 'logins', beta, 'new');
    scenario(script, 'uses', beta, `beta-again-${Date.now()}`);
  });

  await test.step('deleting b without its databases is refused, and nothing changes', async () => {
    await deleteSite(page, urls.beta, beta, false);
    await expect(page.getByText(/still has the databases s_beta_app/)).toBeVisible();
    scenario(script, 'kept', beta);
  });

  await test.step('deleting b with its databases removes its MySQL and Valkey users', async () => {
    scenario(script, 'mark');
    await deleteSite(page, urls.beta, beta, true);
    await expect(page).toHaveURL(`${panel}/admin/sites`);
    scenario(script, 'wait', 'site.destroy', '1', 'succeeded');
    scenario(script, 'gone', beta);
    scenario(script, 'kept', alpha);
  });

  await test.step('deleting a, with its worker still running', async () => {
    scenario(script, 'mark');
    await deleteSite(page, urls.alpha, alpha, true);
    await expect(page).toHaveURL(`${panel}/admin/sites`);
    scenario(script, 'wait', 'site.destroy', '1', 'succeeded');
    scenario(script, 'gone', alpha);
  });

  scenario(script, 'mem', 'both sites deleted');
  scenario(script, 'snapshot', 'after');
});
