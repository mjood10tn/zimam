// v0.4.0's workers, scheduler, cron jobs and Node.js, through the panel as
// the admin, on v0.2.0's Laravel site "laravel": a custom worker is added
// and shows its process running, a cron job is added and shows its next
// runs, the scheduler is turned on and off, the Node.js page shows the
// version e2e/scenarios/v04-workers.sh left installed, and the worker and
// the cron job are deleted again, so the probes' snapshots that follow see
// nothing of them. On the way it takes screenshots of the Workers and
// Schedule tabs and the Node.js page at 400 px and 1280 px into
// $E2E_WORK/screens, for a look at the layout; they aren't checked. The
// worker and cron checks from outside the browser are v04-workers.sh's.
import { mkdirSync } from 'node:fs';
import { expect, type Page } from '@playwright/test';
import { confirm, openTab, panel, signInAsAdmin, test, work } from './support.ts';

const minutes = 60_000;
const site = 'laravel';
const screens = `${work}/screens`;

// untilTab opens the tab again until its content matches want, and fails at
// once on failed: the tab follows the agent job through the panel's queue.
async function untilTab(page: Page, url: string, tab: string, want: RegExp, failed: RegExp, timeout: number): Promise<void> {
  const end = Date.now() + timeout;
  for (;;) {
    await openTab(page, url, tab);
    await page.waitForTimeout(1_000);
    const text = await page.locator('main').innerText();
    if (want.test(text)) {
      return;
    }
    if (failed.test(text)) {
      throw new Error(`the ${tab} tab shows ${failed}:\n${text}`);
    }
    if (Date.now() > end) {
      throw new Error(`no ${want} on the ${tab} tab within ${timeout / 1000} s:\n${text}`);
    }
    await page.waitForTimeout(3_000);
  }
}

// shoot saves the page at a phone's width and a desktop's.
async function shoot(page: Page, name: string, open: () => Promise<void>): Promise<void> {
  mkdirSync(screens, { recursive: true });
  for (const width of [400, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await open();
    await page.waitForTimeout(1_000);
    await page.screenshot({ path: `${screens}/${name}-${width}.png`, fullPage: true });
  }
  await page.setViewportSize({ width: 1280, height: 720 });
}

test('v0.4.0: a worker, a cron job and the scheduler from a site\'s tabs, and Node.js on its page', async ({ page }) => {
  test.setTimeout(30 * minutes);
  await signInAsAdmin(page);

  await page.goto(`${panel}/admin/sites`);
  await page.getByRole('link', { name: site, exact: true }).first().click();
  await expect(page).toHaveURL(/\/admin\/sites\/\d+$/);
  const url = page.url();

  await test.step('a custom worker, added from the Workers tab, runs', async () => {
    await openTab(page, url, 'Workers');
    await page.getByRole('button', { name: 'Add worker', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Add a worker' });
    await dialog.getByRole('textbox', { name: /^Name/ }).fill('sleeper');
    await dialog.getByText('Custom command', { exact: true }).click();
    await dialog.getByRole('textbox', { name: /^Command/ }).fill('sleep 3600');
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.getByText(`Saving the workers of ${site}`)).toBeVisible();
    await untilTab(page, url, 'Workers', /Process 1: running/, /Not applied|failed/, 5 * minutes);
    await expect(page.getByText('Custom · sleep 3600')).toBeVisible();
    await shoot(page, 'workers-tab', () => openTab(page, url, 'Workers'));
  });

  await test.step('a cron job, added from the Schedule tab, shows its next runs; the scheduler turns on', async () => {
    await openTab(page, url, 'Schedule');
    await page.getByRole('button', { name: 'Add cron job', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Add a cron job' });
    await dialog.getByRole('textbox', { name: /^Name/ }).fill('weekly');
    await dialog.getByRole('textbox', { name: /^Schedule/ }).fill('0 3 * * mon');
    await dialog.getByRole('textbox', { name: /^Command/ }).fill('php artisan reports:send');
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.getByText(`Saving the cron jobs of ${site}`)).toBeVisible();
    await untilTab(page, url, 'Schedule', /weekly[\s\S]*Mon \d{1,2} \w{3} \d{4}, 03:00/, /Not applied/, 5 * minutes);
    await expect(page.getByText('0 3 * * mon', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Turn on', exact: true }).click();
    await untilTab(page, url, 'Schedule', /Laravel scheduler[\s\S]*\bOn\b[\s\S]*Turn off/, /Not applied/, 5 * minutes);
    await shoot(page, 'schedule-tab', () => openTab(page, url, 'Schedule'));
  });

  await test.step('the Node.js page shows the installed version', async () => {
    await page.goto(`${panel}/admin/node`);
    await expect(page.getByText(/Node\.js 26\.\d+\.\d+\S* \(major 26\) is installed\./)).toBeVisible();
    await expect(page.getByText(/Security support for Node 26 ends on /)).toBeVisible();
    await shoot(page, 'node-page', () => page.goto(`${panel}/admin/node`).then(() => undefined));
  });

  await test.step('the scheduler goes off, and the cron job and the worker are deleted', async () => {
    await openTab(page, url, 'Schedule');
    await page.getByRole('button', { name: 'Turn off', exact: true }).click();
    await untilTab(page, url, 'Schedule', /Laravel scheduler[\s\S]*\bOff\b[\s\S]*Turn on/, /Not applied/, 5 * minutes);
    await confirm(page, page.getByRole('row', { name: /weekly/ }).getByRole('button', { name: 'Delete', exact: true }), 'Delete cron job weekly?', 'Delete cron job');
    await untilTab(page, url, 'Schedule', /No cron jobs yet\./, /Not applied/, 5 * minutes);
    await openTab(page, url, 'Workers');
    await confirm(page, page.getByRole('row', { name: /sleeper/ }).getByRole('button', { name: 'Delete', exact: true }), 'Delete worker sleeper?', 'Delete worker');
    await untilTab(page, url, 'Workers', /No workers yet\./, /Not applied/, 5 * minutes);
  });
});
