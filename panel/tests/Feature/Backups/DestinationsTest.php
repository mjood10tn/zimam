<?php

namespace Tests\Feature\Backups;

use App\Filament\Pages\Backups;
use App\Filament\Pages\Backups\DestinationsTable;
use App\Filament\Pages\Backups\PlansTable;
use App\Filament\Pages\Backups\RunsTable;
use App\Models\AgentJob;
use App\Models\BackupDestination;
use Illuminate\Support\Facades\Crypt;
use Livewire\Livewire;

/** Backup destinations: add with the keys shown once (R05-D3), test, show the keys again, delete. */
final class DestinationsTest extends BackupsTestCase
{
    private const ACCESS_KEY = 'AKIAEXAMPLEKEY01';

    private const SECRET_KEY = 'wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLE';

    /**
     * The keys the open add dialog holds, sealed with the app key in the
     * component's state; only tests open the seal.
     *
     * @return array{password: string, salt: string}
     */
    private function keysOf(mixed $component): array
    {
        return json_decode(Crypt::decryptString($component->get('sealedKeys')), true);
    }

    /** @return array<string, mixed> the add form for an S3 destination */
    private function s3Form(string $check): array
    {
        return [
            'key' => 'offsite',
            'type' => BackupDestination::S3,
            'endpoint' => self::S3['endpoint'],
            'region' => self::S3['region'],
            'bucket' => self::S3['bucket'],
            'prefix' => self::S3['prefix'],
            'access_key_id' => self::ACCESS_KEY,
            'secret_access_key' => self::SECRET_KEY,
            'check' => $check,
        ];
    }

    public function testPageShowsTheSections(): void
    {
        Livewire::test(Backups::class)
            ->assertSeeLivewire(DestinationsTable::class)
            ->assertSeeLivewire(PlansTable::class)
            ->assertSeeLivewire(RunsTable::class);
        Livewire::test(DestinationsTable::class)->assertSee(__('zimam.backups.destinations.heading'));
    }

    public function testAddDestinationShowsKeysOnceAndStoresNone(): void
    {
        $component = Livewire::test(DestinationsTable::class)->mountAction('add');
        $keys = $this->keysOf($component);
        $this->assertMatchesRegularExpression(BackupDestination::SECRET_PATTERN, $keys['password']);
        $this->assertMatchesRegularExpression(BackupDestination::SECRET_PATTERN, $keys['salt']);
        $this->assertNotSame($keys['password'], $keys['salt']);
        // The state holds them sealed only.
        $this->assertStringNotContainsString($keys['password'], json_encode($component->instance()->all()));
        $modal = $component->getMountedActionModalHtml();
        $this->assertStringContainsString($keys['password'], $modal);
        $this->assertStringContainsString($keys['salt'], $modal);
        $component->fillForm($this->s3Form(substr($keys['password'], -BackupDestination::CHECK_LENGTH)))
            ->callMountedAction()
            ->assertHasNoFormErrors()
            ->assertNotified(__('zimam.backups.destinations.add.submitted', ['key' => 'offsite']))
            ->assertDontSee($keys['password'])
            ->assertSet('sealedKeys', null);

        $this->assertSame([[
            'id' => 'offsite',
            'type' => 's3',
            's3' => self::S3 + ['access_key_id' => self::ACCESS_KEY, 'secret_access_key' => self::SECRET_KEY],
            'encryption_password' => $keys['password'],
            'encryption_salt' => $keys['salt'],
        ]], $this->submitted('backup.destination'));
        $destination = BackupDestination::sole();
        $this->assertSame(BackupDestination::SAVING, $destination->status);
        $this->assertSame(self::S3, $destination->settings);
        $this->assertNoTableHolds($keys['password'], $keys['salt'], self::ACCESS_KEY, self::SECRET_KEY);

        $this->trackToTheEnd(AgentJob::sole());
        $this->assertSame(BackupDestination::ACTIVE, $destination->refresh()->status);
        $this->assertNoTableHolds($keys['password'], $keys['salt'], self::ACCESS_KEY, self::SECRET_KEY);
    }

    public function testAddDestinationNeedsKeySaveCheck(): void
    {
        $component = Livewire::test(DestinationsTable::class)->mountAction('add');
        $keys = $this->keysOf($component);
        $component->fillForm($this->s3Form('xxxxxx'))
            ->callMountedAction()
            ->assertHasFormErrors(['check']);

        $this->assertSame([], $this->submitted('backup.destination'));
        $this->assertSame(0, BackupDestination::count());
        // The dialog stays open with the same keys.
        $this->assertSame($keys, $this->keysOf($component));
    }

    public function testAddDestinationRefusesAPlainHttpEndpointAndADottedPrefix(): void
    {
        $component = Livewire::test(DestinationsTable::class)->mountAction('add');
        $keys = $this->keysOf($component);
        $form = ['endpoint' => 'http://s3.example.com', 'prefix' => 'a/../b'] + $this->s3Form(substr($keys['password'], -6));
        $component->fillForm($form)->callMountedAction()->assertHasFormErrors(['endpoint', 'prefix']);

        $this->assertSame([], $this->submitted('backup.destination'));
    }

    public function testNewSftpDestinationShowsItsPublicKey(): void
    {
        $component = Livewire::test(DestinationsTable::class)->mountAction('add');
        $keys = $this->keysOf($component);
        $component->fillForm([
            'key' => 'box', 'type' => BackupDestination::SFTP, 'host' => 'backup.example.com', 'port' => 23, 'user' => 'u123', 'path' => 'zimam',
            'check' => substr($keys['password'], -6),
        ])->callMountedAction()->assertHasNoFormErrors();

        $this->assertSame(['host' => 'backup.example.com', 'port' => 23, 'user' => 'u123', 'path' => 'zimam'], $this->submitted('backup.destination')[0]['sftp']);
        $this->trackToTheEnd(AgentJob::sole());
        $destination = BackupDestination::sole();
        $this->assertStringStartsWith('ssh-ed25519 ', $destination->public_key);
        Livewire::test(DestinationsTable::class)->assertSee($destination->public_key);
    }

    public function testTestDestinationShowsFingerprint(): void
    {
        $destination = $this->destination('box', BackupDestination::SFTP);

        Livewire::test(DestinationsTable::class)
            ->callTableAction('test', $destination)
            ->assertNotified(__('zimam.backups.destinations.test.submitted', ['key' => 'box']));

        $this->assertSame([['id' => 'box']], $this->submitted('backup.destination.test'));
        $this->trackToTheEnd(AgentJob::sole());
        $destination->refresh();
        $this->assertMatchesRegularExpression(BackupDestination::FINGERPRINT_PATTERN, $destination->host_key_fingerprint);
        $this->assertNotNull($destination->last_tested_at);
        Livewire::test(DestinationsTable::class)->assertSee($destination->host_key_fingerprint);
    }

    public function testFailedTestKeepsTheReason(): void
    {
        $destination = $this->destination();
        $this->agent->scriptJob('backup.destination.test', ['running', 'failed'], errorCode: 'job_failed', message: 'rclone: 403 Forbidden');

        Livewire::test(DestinationsTable::class)->callTableAction('test', $destination);
        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame('rclone: 403 Forbidden', $destination->refresh()->last_error);
        $this->assertNull($destination->last_tested_at);
        Livewire::test(DestinationsTable::class)->assertSee('rclone: 403 Forbidden');
    }

    public function testShowKeysAgainNeedsPasswordAndStoresNone(): void
    {
        $destination = $this->destination();
        $password = str_repeat('p', 43);

        Livewire::test(DestinationsTable::class)
            ->callTableAction('keys', $destination, ['current_password' => 'not the password'])
            ->assertHasTableActionErrors(['current_password']);
        $this->assertSame([], $this->submitted('backup.destination.keys'));

        $component = Livewire::test(DestinationsTable::class)
            ->callTableAction('keys', $destination, ['current_password' => self::PASSWORD])
            ->assertSee($password)
            ->assertSee(str_repeat('s', 43));
        $this->assertStringNotContainsString($password, json_encode($component->instance()->all()));
        $component->call('closeKeys')->assertDontSee($password);

        $this->assertSame([['id' => 'offsite']], $this->submitted('backup.destination.keys'));
        // The read is in agent_actions, as the agent audits it, without the keys.
        $this->assertDatabaseHas('agent_actions', ['command' => 'backup.destination.keys', 'outcome' => 'ok']);
        $this->assertNoTableHolds($password, str_repeat('s', 43));
    }

    public function testDeleteIsBlockedWhileAPlanUsesIt(): void
    {
        $destination = $this->destination();
        $this->plan($destination);

        Livewire::test(DestinationsTable::class)->assertTableActionDisabled('delete', $destination);
    }

    public function testDeleteNeedsPasswordAndRemovesTheRow(): void
    {
        $destination = $this->destination();

        Livewire::test(DestinationsTable::class)
            ->callTableAction('delete', $destination, ['current_password' => self::PASSWORD])
            ->assertHasNoTableActionErrors();
        $this->assertSame(BackupDestination::DELETING, $destination->refresh()->status);

        $this->trackToTheEnd(AgentJob::sole());
        $this->assertModelMissing($destination);
    }
}
