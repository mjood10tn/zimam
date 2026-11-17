<?php

namespace Tests\Feature\Databases;

use App\Agent\Exceptions\AgentConflict;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\Site;
use App\Models\SiteCredential;
use Livewire\Features\SupportTesting\Testable;
use Livewire\Livewire;

/** Deleting a site that has databases: the owner chooses to drop them, and the agent refuses otherwise. */
final class DeleteSiteDatabasesTest extends DatabasesTestCase
{
    private function siteView(Site $site): Testable
    {
        return Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()]);
    }

    public function testDeleteSiteWithDatabasesAsksToDropThem(): void
    {
        $site = $this->site();
        $this->database($site, 'main');
        $this->database($site, 'extra');

        // The box is offered, naming the databases.
        $this->siteView($site)
            ->mountAction('delete')
            ->assertMountedActionModalSee(__('zimam.sites.delete.drop_databases'))
            ->assertMountedActionModalSee('s_shop_extra, s_shop_main');

        $this->siteView($site)
            ->callAction('delete', ['current_password' => self::PASSWORD, 'drop_databases' => true])
            ->assertHasNoActionErrors();

        $this->assertSame([['name' => 'shop', 'drop_databases' => true]], $this->submitted('site.destroy'));
        $this->assertSame(['name' => 'shop', 'drop_databases' => true], AgentJob::sole()->params);

        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame(0, Site::count());
        $this->assertSame(0, Database::count());
        $this->assertSame([], $this->agent->databaseNames());
    }

    /** Without the box the agent refuses, and its words are shown; the site stays as it was. */
    public function testDeleteWithoutTheBoxShowsTheAgentsRefusal(): void
    {
        $site = $this->site();
        $this->database($site, 'main');

        $this->siteView($site)
            ->callAction('delete', ['current_password' => self::PASSWORD, 'drop_databases' => false])
            ->assertNotified('Site shop still has databases. Delete them first, or delete the site with its databases.');

        $this->assertSame([['name' => 'shop']], $this->submitted('site.destroy'));
        $this->assertSame(Site::ACTIVE, $site->refresh()->status);
        $this->assertSame(1, Database::count());
    }

    /** MySQL down or not installed while the site has a MySQL user: the agent's message is shown. */
    public function testAgentRefusalWhileMysqlIsDownIsShown(): void
    {
        $site = $this->site();
        $this->agent->respond('site.destroy', new AgentConflict('MySQL isn’t running, so the site’s MySQL user can’t be removed. Start MySQL and try again.'));

        $this->siteView($site)
            ->callAction('delete', ['current_password' => self::PASSWORD])
            ->assertNotified('MySQL isn’t running, so the site’s MySQL user can’t be removed. Start MySQL and try again.');

        $this->assertSame(Site::ACTIVE, $site->refresh()->status);
    }

    /** A site with nothing in MySQL deletes exactly as before: no box, no new param. */
    public function testSiteWithoutDatabasesDeletesAsBefore(): void
    {
        $site = $this->site();
        SiteCredential::create(['site_id' => $site->id, 'kind' => SiteCredential::MYSQL, 'username' => 's_shop']);

        $this->siteView($site)
            ->mountAction('delete')
            ->assertMountedActionModalDontSee(__('zimam.sites.delete.drop_databases'));
        $this->siteView($site)->callAction('delete', ['current_password' => self::PASSWORD, 'drop_databases' => true]);

        $this->assertSame([['name' => 'shop']], $this->submitted('site.destroy'));
        $this->trackToTheEnd(AgentJob::sole());
        $this->assertSame(0, Site::count());
        $this->assertSame(0, SiteCredential::count());
    }

    /** A drop that failed part way leaves the site failed, and the list says which databases are left. */
    public function testFailedDestroySyncsTheDatabases(): void
    {
        $site = $this->site();
        $this->database($site, 'main');
        $this->database($site, 'extra');
        $this->agent->scriptJob('site.destroy', ['running', 'failed'], errorCode: 'internal', message: 'DROP DATABASE s_shop_main failed');
        $this->siteView($site)->callAction('delete', ['current_password' => self::PASSWORD, 'drop_databases' => true]);
        // Part of it happened: the agent dropped one database before failing.
        $this->agent->respond('db.list', ['databases' => [['name' => 's_shop_main', 'site' => 'shop', 'size_bytes' => 0]]]);

        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame(Site::FAILED, $site->refresh()->status);
        $this->assertSame(['s_shop_main'], Database::pluck('name')->all());
    }

    /**
     * Two sites with both service users and a rotation date each; the first is
     * the one deleted. Returns it.
     */
    private function sitesWithCredentials(): Site
    {
        $site = $this->site();
        $other = $this->site('blog');
        foreach ([$site, $other] as $s) {
            foreach ([SiteCredential::MYSQL, SiteCredential::VALKEY] as $kind) {
                SiteCredential::create(['site_id' => $s->id, 'kind' => $kind, 'username' => "s_{$s->name}", 'rotated_at' => now()]);
            }
        }

        return $site;
    }

    /**
     * @param  list<string>  $states
     * @return list<string> the kinds of credential the site still has
     */
    private function destroyEnding(Site $site, array $states, mixed $result): array
    {
        $this->agent->scriptJob('site.destroy', $states, result: $result, errorCode: 'internal', message: 'removing the site failed');
        $this->siteView($site)->callAction('delete', ['current_password' => self::PASSWORD]);
        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame(Site::FAILED, $site->refresh()->status);
        $this->assertSame(2, SiteCredential::where('site_id', Site::where('name', 'blog')->value('id'))->count(), 'another site\'s rows are kept');

        return SiteCredential::where('site_id', $site->id)->orderBy('kind')->pluck('kind')->all();
    }

    /** The agent says which users a failed destroy removed; only those rows go. */
    public function testFailedDestroyForgetsOnlyTheUsersTheAgentRemoved(): void
    {
        $site = $this->sitesWithCredentials();

        $left = $this->destroyEnding($site, ['running', 'failed'], ['name' => 'shop', 'removed' => ['mysql_user' => false, 'valkey_user' => true]]);

        $this->assertSame([SiteCredential::MYSQL], $left);
        $this->assertNotNull(SiteCredential::where('site_id', $site->id)->where('kind', SiteCredential::MYSQL)->sole()->rotated_at, 'the user that stayed keeps its date');
    }

    public function testFailedDestroyBeforeAnyUserWasRemovedKeepsBothRows(): void
    {
        $site = $this->sitesWithCredentials();

        $left = $this->destroyEnding($site, ['running', 'failed'], ['name' => 'shop', 'removed' => ['mysql_user' => false, 'valkey_user' => false]]);

        $this->assertSame([SiteCredential::MYSQL, SiteCredential::VALKEY], $left);
    }

    public function testFailedDestroyAfterBothUsersWereRemovedForgetsBothRows(): void
    {
        $site = $this->sitesWithCredentials();

        $left = $this->destroyEnding($site, ['running', 'failed'], ['name' => 'shop', 'removed' => ['mysql_user' => true, 'valkey_user' => true]]);

        $this->assertSame([], $left);
    }

    /** An agent that doesn't say (one from before the field) is answered as before: the users may be gone. */
    public function testFailedDestroyWithoutAnAnswerForgetsTheRows(): void
    {
        $site = $this->sitesWithCredentials();

        $left = $this->destroyEnding($site, ['running', 'failed'], null);

        $this->assertSame([], $left);
    }

    /** A destroy cancelled while it was still queued never ran: nothing is forgotten. */
    public function testDestroyCancelledBeforeItRanForgetsNothing(): void
    {
        $site = $this->sitesWithCredentials();
        $this->agent->scriptJob('site.destroy', ['cancelled']);
        $this->siteView($site)->callAction('delete', ['current_password' => self::PASSWORD]);
        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame(Site::FAILED, $site->refresh()->status);
        $this->assertSame(2, SiteCredential::where('site_id', $site->id)->count());
    }

    /** One cancelled after it started may have got anywhere, and says nothing. */
    public function testDestroyCancelledAfterItStartedForgetsTheRows(): void
    {
        $site = $this->sitesWithCredentials();
        $this->agent->scriptJob('site.destroy', ['running', 'cancelled']);
        $this->siteView($site)->callAction('delete', ['current_password' => self::PASSWORD]);
        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame(0, SiteCredential::where('site_id', $site->id)->count());
    }
}
