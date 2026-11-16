// v0.2.0's deploys (done-when 1 to 8), through the panel as the admin: a
// Laravel site connects zimam-e2e/laravel-app on the run's Git host
// (e2e/githost, playing GitHub), deploys it, has its .env edited,
// deploys again under a request loop, keeps its live release through a
// failing script, rolls back, and deploys on a signed webhook and on its
// deploy URL; its Laravel log shows the app's line. On the way, the
// one-time reveals are checked in a real browser: the webhook secret and
// the deploy URL stay on screen through polls, a Regenerate replaces
// them, and a reload hides them; the .env shows only after the password,
// a refused save keeps what was typed, and a reload hides it. The checks
// from outside the browser are e2e/scenarios/v02-deploy.sh's.
import { readFileSync, writeFileSync } from 'node:fs';
import { expect, type Locator, type Page, type Response } from '@playwright/test';
import { domain, loadAdmin, panel, scenario, signInAsAdmin, untilText, work, test } from './support.ts';

const minutes = 60_000;
const site = 'laravel';

const secretCode = (page: Page) => page.locator('[wire\\:key^="webhook-secret-"] code');
const deployUrlCode = (page: Page) => page.locator('[wire\\:key^="deploy-url-"] code');

// livewireUpdate waits for the page's next Livewire round trip, such as the
// tab's 5-second poll.
function livewireUpdate(page: Page, timeout = 20_000): Promise<Response> {
  return page.waitForResponse((r) => r.request().method() === 'POST' && /\/livewire[^/]*\/update/.test(r.url()), { timeout });
}

async function openTab(page: Page, url: string, tab: string): Promise<void> {
  await page.goto(url);
  await page.getByRole('tab', { name: tab, exact: true }).click();
}

// livewireCall waits for the Livewire round trip that carries a call to
// method, such as Filament's mountAction.
function livewireCall(page: Page, method: string, timeout = 30_000): Promise<Response> {
  return page.waitForResponse(
    (r) =>
      r.request().method() === 'POST' &&
      /\/livewire[^/]*\/update/.test(r.url()) &&
      (r.request().postData() ?? '').includes(`"method":"${method}"`),
    { timeout },
  );
}

// confirm clicks an action's trigger, fills the password dialog it opens and
// submits it. Filament shows the dialog at once but renders its form from
// the mountAction answer: a password typed before that answer is wiped,
// which over a real server's latency (vps.yml) left the field empty and the
// browser's required check stopped the submit. So the fill waits for the
// answer, the value is checked, and the submit must reach the server.
async function confirm(page: Page, trigger: Locator, heading: string | RegExp, submit: string): Promise<void> {
  const mounted = livewireCall(page, 'mountAction');
  await trigger.click();
  await mounted;
  const dialog = page.getByRole('dialog', { name: heading });
  const field = dialog.getByLabel('Current password');
  await field.fill(loadAdmin().password);
  await expect(field).toHaveValue(loadAdmin().password);
  const called = livewireCall(page, 'callMountedAction');
  await dialog.getByRole('button', { name: submit, exact: true }).click();
  await called;
  await expect(dialog).toBeHidden();
}

async function deployNow(page: Page, url: string): Promise<void> {
  await openTab(page, url, 'Deployments');
  scenario('v02-deploy.sh', 'mark');
  await page.getByRole('button', { name: 'Deploy now', exact: true }).click();
  await expect(page.getByText(`Deploying ${site}`)).toBeVisible();
}

async function saveScript(page: Page, url: string, script: string): Promise<void> {
  await openTab(page, url, 'Deployments');
  await page.getByRole('textbox', { name: /^Deploy script/ }).fill(script);
  await page.getByRole('button', { name: 'Save settings', exact: true }).click();
  await expect(page.getByText('Deploy settings saved')).toBeVisible();
}

test('v0.2.0: a Laravel site deploys, rolls back, and deploys on a webhook and its deploy URL', async ({ page }) => {
  test.setTimeout(45 * minutes);
  await signInAsAdmin(page);

  let url = '';
  await test.step('done-when 7: a Laravel site', async () => {
    await page.goto(`${panel}/admin/sites/create`);
    await page.getByRole('textbox', { name: /^Name/ }).fill(site);
    await page.getByRole('textbox', { name: /^Domain/ }).fill(`${site}.${domain}`);
    await page.locator('label[for="form.type-laravel"]').click();
    await page.getByRole('combobox', { name: /^PHP version/ }).selectOption('8.4');
    await page.getByRole('button', { name: 'Create', exact: true }).click();
    await expect(page).toHaveURL(/\/admin\/sites\/\d+$/);
    url = page.url();
    await untilText(page, /Status\s+Active/, /Status\s+Failed/, 5 * minutes);
  });

  let secret = '';
  let deployUrl = '';
  await test.step('done-when 1: connect the repository; the secret and the deploy URL show once', async () => {
    await openTab(page, url, 'Deployments');
    await page.getByRole('textbox', { name: /^GitHub repository/ }).fill('zimam-e2e/laravel-app');
    await page.getByRole('textbox', { name: /^Branch/ }).fill('main');
    await page.getByRole('button', { name: 'Connect repository', exact: true }).click();
    await expect(secretCode(page)).toHaveText(/^[0-9a-f]{64}$/);
    await expect(deployUrlCode(page)).toHaveText(new RegExp(`^${panel.replace(/\./g, '\\.')}/hooks/deploy/${site}/[0-9a-f]{64}$`));
    secret = (await secretCode(page).innerText()).trim();
    deployUrl = (await deployUrlCode(page).innerText()).trim();
    // site.repo runs, so the tab polls every 5 s; both stay through a poll.
    await livewireUpdate(page);
    await expect(secretCode(page)).toHaveText(secret);
    await expect(deployUrlCode(page)).toHaveText(deployUrl);
    // The deploy key arrives by the poll too, made as the site user.
    await expect(page.locator('pre').filter({ hasText: /^ssh-ed25519 [A-Za-z0-9+/]+=*$/ })).toBeVisible({ timeout: 10 * minutes });
    await expect(secretCode(page)).toHaveText(secret);
    await expect(deployUrlCode(page)).toHaveText(deployUrl);
  });

  await test.step('Regenerate replaces the shown secret and URL; a reload hides both', async () => {
    await confirm(page, page.getByRole('button', { name: 'Regenerate secret', exact: true }), 'Regenerate the webhook secret?', 'Regenerate secret');
    await expect(secretCode(page)).toHaveText(/^[0-9a-f]{64}$/);
    await expect(secretCode(page)).not.toHaveText(secret);
    const newSecret = (await secretCode(page).innerText()).trim();

    await confirm(page, page.getByRole('button', { name: 'Regenerate URL', exact: true }), 'Regenerate the deploy URL?', 'Regenerate URL');
    await expect(deployUrlCode(page)).not.toHaveText(deployUrl);
    await expect(deployUrlCode(page)).toHaveText(new RegExp(`/hooks/deploy/${site}/[0-9a-f]{64}$`));
    const newDeployUrl = (await deployUrlCode(page).innerText()).trim();
    // The second Regenerate was a round trip of its own: the new secret stayed.
    await expect(secretCode(page)).toHaveText(newSecret);

    writeFileSync(`${work}/v02-webhook-secret`, newSecret, { mode: 0o600 });
    writeFileSync(`${work}/v02-deploy-url`, newDeployUrl, { mode: 0o600 });
    writeFileSync(`${work}/v02-deploy-url-replaced`, deployUrl, { mode: 0o600 });

    await page.reload();
    await page.getByRole('tab', { name: 'Deployments', exact: true }).click();
    await expect(page.getByText('Shown only once, when it was made. Regenerate it to see a new one.')).toHaveCount(2);
    await expect(secretCode(page)).toHaveCount(0);
    await expect(deployUrlCode(page)).toHaveCount(0);
    const html = await page.content();
    expect(html).not.toContain(newSecret);
    expect(html).not.toContain(newDeployUrl.split('/').pop() ?? newDeployUrl);
  });

  await test.step('the deploy key fetches from the Git host; auto-deploy on, the Laravel preset kept', async () => {
    scenario('v02-deploy.sh', 'authorize');
    await openTab(page, url, 'Deployments');
    await expect(page.getByRole('textbox', { name: /^Deploy script/ })).toHaveValue(/php artisan config:cache/);
    const auto = page.getByRole('switch', { name: 'Auto-deploy', exact: true });
    if ((await auto.getAttribute('aria-checked')) !== 'true') {
      await auto.click();
    }
    await expect(auto).toHaveAttribute('aria-checked', 'true');
    await expect(page.getByRole('switch', { name: 'Run Composer', exact: true })).toHaveAttribute('aria-checked', 'true');
    await page.getByRole('button', { name: 'Save settings', exact: true }).click();
    await expect(page.getByText('Deploy settings saved')).toBeVisible();
  });

  await test.step('done-when 2 and 7: the first deploy, with Composer and the Laravel preset', async () => {
    await deployNow(page, url);
    scenario('v02-deploy.sh', 'wait', 'site.deploy', 'succeeded', 'manual');
    scenario('v02-deploy.sh', 'serves', 'v1');
  });

  const greeting = `hello-${Date.now()}`;
  await test.step('done-when 8: the .env editor, after the password', async () => {
    await openTab(page, url, 'Environment');
    // The tab has rendered, with the editor hidden behind the password.
    await expect(page.getByRole('button', { name: 'Show and edit .env', exact: true })).toBeVisible();
    await expect(page.getByRole('textbox', { name: 'Contents' })).toHaveCount(0);
    await confirm(page, page.getByRole('button', { name: 'Show and edit .env', exact: true }), 'Show the .env', 'Show and edit .env');
    const editor = page.getByRole('textbox', { name: 'Contents' });
    await expect(editor).toHaveValue(/^APP_KEY=base64:[A-Za-z0-9+/]{43}=$/m);
    await expect(editor).toHaveValue(/^APP_ENV=production$/m);
    const env = await editor.inputValue();
    writeFileSync(`${work}/v02-env-marker`, greeting, { mode: 0o600 });
    scenario('v02-deploy.sh', 'mark');
    await editor.fill(`${env.replace(/\n*$/, '')}\nE2E_GREETING=${greeting}\n`);
    await page.getByRole('button', { name: 'Save .env', exact: true }).click();
    await expect(page.getByText(`Saving ${site}’s .env`)).toBeVisible();
    await expect(editor).toHaveCount(0);
    scenario('v02-deploy.sh', 'wait', 'site.env.write', 'succeeded');
    scenario('v02-deploy.sh', 'env', greeting);
  });

  await test.step('a refused save keeps the typed text; a reload hides the .env', async () => {
    await openTab(page, url, 'Environment');
    await confirm(page, page.getByRole('button', { name: 'Show and edit .env', exact: true }), 'Show the .env', 'Show and edit .env');
    const editor = page.getByRole('textbox', { name: 'Contents' });
    await expect(editor).toHaveValue(new RegExp(`^E2E_GREETING=${greeting}$`, 'm'));
    const tooLong = `E2E_PAD=${'x'.repeat(65536 - 'E2E_PAD='.length)}\n`;
    expect(Buffer.byteLength(tooLong)).toBe(65537);
    await editor.fill(tooLong);
    await page.getByRole('button', { name: 'Save .env', exact: true }).click();
    await expect(page.getByText('The .env can be at most 65536 bytes.')).toBeVisible();
    await expect(editor).toHaveValue(tooLong);
    scenario('v02-deploy.sh', 'env', greeting);

    await page.reload();
    await page.getByRole('tab', { name: 'Environment', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Show and edit .env', exact: true })).toBeVisible();
    await expect(page.getByRole('textbox', { name: 'Contents' })).toHaveCount(0);
    const html = await page.content();
    expect(html).not.toContain('APP_KEY=');
    expect(html).not.toContain(greeting);
  });

  await test.step('done-when 2: a deploy under a request loop, with the new .env', async () => {
    scenario('v02-deploy.sh', 'commit', 'v2');
    scenario('v02-deploy.sh', 'loop', 'start');
    try {
      await deployNow(page, url);
      scenario('v02-deploy.sh', 'wait', 'site.deploy', 'succeeded', 'manual');
    } finally {
      scenario('v02-deploy.sh', 'loop', 'stop', 'v1', 'v2');
    }
    scenario('v02-deploy.sh', 'serves', 'v2', greeting);
  });

  await test.step('done-when 3: a failing script leaves the live release serving', async () => {
    const preset = await (async () => {
      await openTab(page, url, 'Deployments');
      return page.getByRole('textbox', { name: /^Deploy script/ }).inputValue();
    })();
    scenario('v02-deploy.sh', 'commit', 'v3');
    await saveScript(page, url, `${preset}\necho e2e: this script fails\nexit 3\n`);
    await deployNow(page, url);
    scenario('v02-deploy.sh', 'wait', 'site.deploy', 'failed', 'manual');
    scenario('v02-deploy.sh', 'serves', 'v2', greeting);
    await saveScript(page, url, preset);
  });

  await test.step('done-when 6: roll back to the first release', async () => {
    await openTab(page, url, 'Deployments');
    const rollback = page.getByRole('button', { name: 'Roll back', exact: true });
    await expect(rollback).toHaveCount(1);
    scenario('v02-deploy.sh', 'mark');
    await confirm(page, rollback, /^Roll back to \d{14}-[0-9a-f]{7}\?$/, 'Roll back');
    await expect(page.getByText(new RegExp(`Rolling ${site} back to`))).toBeVisible();
    scenario('v02-deploy.sh', 'wait', 'site.rollback', 'succeeded');
    // The first release's cached config has no greeting yet.
    scenario('v02-deploy.sh', 'serves', 'v1');
  });

  await test.step('done-when 4: a signed GitHub push deploys its commit, not the branch head', async () => {
    // v5 is pushed on top of v4 before the delivery for v4 arrives.
    scenario('v02-deploy.sh', 'commit', 'v4');
    scenario('v02-deploy.sh', 'commit', 'v5');
    scenario('v02-deploy.sh', 'mark');
    scenario('v02-deploy.sh', 'webhook', 'v4');
    scenario('v02-deploy.sh', 'wait', 'site.deploy', 'succeeded', 'webhook');
    scenario('v02-deploy.sh', 'serves', 'v4', greeting);
  });

  await test.step('done-when 5: the deploy URL deploys the branch head', async () => {
    scenario('v02-deploy.sh', 'mark');
    scenario('v02-deploy.sh', 'deploy-url');
    scenario('v02-deploy.sh', 'wait', 'site.deploy', 'succeeded', 'url');
    scenario('v02-deploy.sh', 'serves', 'v5', greeting);
  });

  await test.step('done-when 7: the Laravel log in the viewer shows the app’s line', async () => {
    scenario('v02-deploy.sh', 'log');
    const word = readFileSync(`${work}/v02-log-word`, 'utf8').trim();
    await openTab(page, url, 'Logs');
    await page.getByRole('combobox', { name: 'Log' }).selectOption({ label: 'Laravel log' });
    await expect(page.getByText(`zimam-e2e ${word}`)).toBeVisible({ timeout: 30_000 });
  });
});
