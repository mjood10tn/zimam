<?php

namespace Tests\Unit\Agent;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;
use App\Agent\Commands\AgentVersion;
use App\Agent\Commands\BackupDestination;
use App\Agent\Commands\BackupDestinationDelete;
use App\Agent\Commands\BackupDestinationKeys;
use App\Agent\Commands\BackupDestinationTest;
use App\Agent\Commands\BackupList;
use App\Agent\Commands\BackupPlan;
use App\Agent\Commands\BackupPlanDelete;
use App\Agent\Commands\BackupRestore;
use App\Agent\Commands\BackupRun;
use App\Agent\Commands\BackupRuns;
use App\Agent\Commands\CacheUser;
use App\Agent\Commands\CertIssue;
use App\Agent\Commands\ChangeConfirm;
use App\Agent\Commands\ChangeRevert;
use App\Agent\Commands\CloudflareMode;
use App\Agent\Commands\DbCreate;
use App\Agent\Commands\DbDrop;
use App\Agent\Commands\DbList;
use App\Agent\Commands\DbUser;
use App\Agent\Commands\E2eSleep;
use App\Agent\Commands\Fail2banConfig;
use App\Agent\Commands\Fail2banStatus;
use App\Agent\Commands\Fail2banUnban;
use App\Agent\Commands\FirewallRules;
use App\Agent\Commands\FirewallStatus;
use App\Agent\Commands\JobCancel;
use App\Agent\Commands\JobFollow;
use App\Agent\Commands\JobStatus;
use App\Agent\Commands\LogsList;
use App\Agent\Commands\LogsPolicy;
use App\Agent\Commands\LogsRead;
use App\Agent\Commands\MysqlInstall;
use App\Agent\Commands\NodeInstall;
use App\Agent\Commands\NodeUninstall;
use App\Agent\Commands\PhpDefault;
use App\Agent\Commands\PhpInstall;
use App\Agent\Commands\PhpUninstall;
use App\Agent\Commands\SecurityCheck;
use App\Agent\Commands\SiteCron;
use App\Agent\Commands\SiteDeploy;
use App\Agent\Commands\SiteDestroy;
use App\Agent\Commands\SiteEnvRead;
use App\Agent\Commands\SiteEnvWrite;
use App\Agent\Commands\SitePhp;
use App\Agent\Commands\SiteProvision;
use App\Agent\Commands\SiteReleases;
use App\Agent\Commands\SiteRepo;
use App\Agent\Commands\SiteRollback;
use App\Agent\Commands\SiteSecurity;
use App\Agent\Commands\SiteWorkers;
use App\Agent\Commands\SiteWorkersRestart;
use App\Agent\Commands\SiteWorkersStatus;
use App\Agent\Commands\SshSettings;
use App\Agent\Commands\SystemMetrics;
use App\Agent\Commands\SystemReboot;
use App\Agent\Commands\SystemRebootCancel;
use App\Agent\Commands\SystemStatus;
use App\Agent\Commands\UpdatesAuto;
use App\Agent\Commands\UpdatesInstall;
use App\Agent\Commands\ValkeyInstall;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentNotFound;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Agent\FakeAgentClient;
use Opis\JsonSchema\Errors\ErrorFormatter;
use Opis\JsonSchema\Validator;
use PHPUnit\Framework\Attributes\DataProvider;
use Tests\Support\SecretCommand;
use Tests\TestCase;

/**
 * The panel's half of the contract (design §6): what it sends and what its
 * fake answers must match the schemas generated from the agent's Go types.
 */
final class SchemaTest extends TestCase
{
    private const JOB = '01JBX7Q3A1B2C3D4E5F6G7H8J9';

    /**
     * The agent's RuntimeMaxSec for every job command, read from the files that
     * define them (the e2e-only commands included), so no second list can drift.
     *
     * @return array<string, int>
     */
    private static function agentRuntimeLimits(): array
    {
        $limits = [];
        foreach (['limits.go', 'limits_e2e.go'] as $file) {
            $source = file_get_contents(base_path("../agent/internal/jobs/{$file}"));
            preg_match_all('/(?:^\s*|limits\[)"([a-z0-9.]+)"\]?(?::|\s*=)\s*(?:Limits)?\{[^}]*RuntimeMaxSec:\s*(\d+)/m', $source, $hits, PREG_SET_ORDER);
            foreach ($hits as $hit) {
                $limits[$hit[1]] = (int) $hit[2];
            }
        }

        return $limits;
    }

    /** @return array<string, array{AgentCommand}> one of every command the panel sends */
    public static function commands(): array
    {
        $commands = [
            new AgentVersion,
            new SystemStatus,
            new SystemMetrics,
            new JobStatus(self::JOB),
            new JobFollow(self::JOB, 0),
            new JobCancel(self::JOB),
            new E2eSleep(5, 'shop'),
            new PhpInstall('8.4'),
            new PhpUninstall('8.3'),
            new PhpDefault('8.5'),
            new SiteProvision('shop', 'shop.example.com', 'php', '8.4'),
            new SitePhp('shop', '8.5'),
            new SiteSecurity('shop', forceHttps: true, hstsMaxAge: 31536000, hstsIncludeSubdomains: true, ipAllow: ['192.0.2.0/24'], ipDeny: ['2001:db8::1'], passwordProtection: true, users: [['username' => 'alice', 'password_hash' => '$2y$12$Mda0LhSrcwEX8fAIrkvr/eP5OR7zhBWHVUNmE9n4xm8B76JOYiAI.']]),
            new SiteDestroy('shop'),
            new MysqlInstall,
            new DbList,
            new DbCreate('shop', 's_shop_main'),
            new DbDrop('shop', 's_shop_main'),
            new DbUser('shop', DbUser::generatePassword()),
            new ValkeyInstall,
            new CacheUser('shop', CacheUser::generatePassword()),
            new CertIssue('shop'),
            new SiteRepo('shop', 'octo/shop', 'release/2.x', rotateKey: true),
            new SiteDeploy('shop', 'manual', composer: true, keepReleases: 5, script: "php artisan migrate --force\n", commit: str_repeat('a1', 20)),
            new SiteRollback('shop', '20261116120000-abcdef1'),
            new SiteReleases('shop'),
            new SiteEnvRead('shop'),
            new SiteEnvWrite('shop', "APP_KEY=base64:dummy\nDB_PASSWORD=\"not a real one\"\n"),
            new SecurityCheck('shop'),
            new LogsList('site.nginx-access', 'shop'),
            new LogsPolicy(14, 365, 5 * 1024 ** 3),
            new LogsRead('journal.nginx', 'back', cursor: 's=a;i=1', limit: 50, search: 'error', priority: 3),
            new SiteWorkers('shop', self::workerSpecs()),
            new SiteWorkersStatus('shop'),
            new SiteWorkersRestart('shop', 'queue'),
            new SiteCron('shop', true, self::cronJobs()),
            new NodeInstall('26'),
            new NodeUninstall,
            new BackupDestination('offsite', 's3', self::S3, str_repeat('a', 43), str_repeat('b', 43)),
            new BackupDestinationDelete('offsite'),
            new BackupDestinationTest('offsite'),
            new BackupDestinationKeys('offsite'),
            new BackupPlan('nightly', 'offsite', ['shop'], ['s_shop_main'], true, 'weekly mon 02:30', ['keep_last' => 7, 'keep_daily' => 7, 'keep_weekly' => 4, 'keep_monthly' => 6]),
            new BackupPlanDelete('nightly'),
            new BackupRun('nightly'),
            new BackupList('nightly'),
            new BackupRuns('nightly', '20261123T020000Z'),
            new BackupRestore('nightly', '20261123T020000Z', 'db-s_shop_main', 'shop', 's_shop_main', overwrite: true),
            new FirewallRules([
                ['id' => 'app', 'action' => 'allow', 'proto' => 'tcp', 'port_from' => 8080, 'port_to' => 8080, 'from' => ['203.0.113.0/24', '2001:db8::7']],
                ['id' => 'games', 'action' => 'limit', 'proto' => 'udp', 'port_from' => 27000, 'port_to' => 27015, 'from' => []],
            ]),
            new FirewallStatus,
            new SshSettings(true, 'prohibit-password', [22, 2222], 6, 120),
            new ChangeConfirm(self::JOB),
            new ChangeRevert(self::JOB),
            new Fail2banConfig(self::fail2banJails(), ['203.0.113.0/24', '2001:db8::7']),
            new Fail2banStatus,
            new Fail2banUnban('zimam-panel', '192.0.2.44'),
            new CloudflareMode(true, true),
            new UpdatesAuto(true, true, '03:30'),
            new UpdatesInstall('all'),
            new SystemReboot('03:30'),
            new SystemRebootCancel,
        ];

        return array_combine(
            array_map(fn (AgentCommand $c): string => $c->name(), $commands),
            array_map(fn (AgentCommand $c): array => [$c], $commands),
        );
    }

    /** @return list<array<string, mixed>> a custom worker and a Laravel one with every option, as SiteWorker::spec() builds them */
    private static function workerSpecs(): array
    {
        return [
            ['id' => 'horizon', 'kind' => 'custom', 'processes' => 1, 'memory_mb' => 1024, 'command' => 'php artisan horizon'],
            ['id' => 'queue', 'kind' => 'laravel-queue', 'processes' => 10, 'memory_mb' => 512, 'connection' => 'redis', 'queues' => ['high', 'emails:low'], 'tries' => 3, 'timeout' => 0, 'sleep' => 3, 'max_time' => 3600, 'backoff' => 10],
        ];
    }

    /** @return list<array<string, mixed>> an enabled job on a macro and a paused one on a full schedule, as SiteCronJob::spec() builds them */
    private static function cronJobs(): array
    {
        return [
            ['id' => 'report', 'schedule' => '@daily', 'command' => 'php artisan report', 'enabled' => true],
            ['id' => 'tidy', 'schedule' => '0,30 */2 1-15/3 jan,jul sat-sun', 'command' => 'php artisan tidy', 'enabled' => false],
        ];
    }

    /** site.cron with the least it takes: no jobs, no scheduler; and the fake's answer. */
    public function testCronParamsAndFakeResultMatchSchemas(): void
    {
        $empty = new SiteCron('shop', false, []);
        $this->assertValid(self::wire($empty->jsonParams()), file_get_contents($this->schemaPath('site.cron.params')), 'site.cron');
        $fake = new FakeAgentClient;
        $final = $fake->follow($fake->submit(new SiteCron('shop', true, self::cronJobs()))->id, 0, function (): void {});
        $this->assertSame('succeeded', $final->status);
        $this->assertResult('site.cron', $final->result, 'fake site.cron result');
    }

    /** The worker commands with the least they take: no workers, every worker restarted. */
    public function testMinimalWorkerParamsMatchSchema(): void
    {
        foreach ([new SiteWorkers('shop', []), new SiteWorkersRestart('shop')] as $command) {
            $this->assertValid(self::wire($command->jsonParams()), file_get_contents($this->schemaPath($command->name().'.params')), $command->name());
        }
    }

    /** The fake's worker answers, and system.metrics once a site has workers. */
    public function testFakeWorkerResultsMatchSchemas(): void
    {
        $fake = new FakeAgentClient;
        foreach ([new SiteWorkers('shop', self::workerSpecs()), new SiteWorkersRestart('shop', 'queue'), new SiteWorkersRestart('shop')] as $command) {
            $final = $fake->follow($fake->submit($command)->id, 0, function (): void {});
            $this->assertSame('succeeded', $final->status);
            $this->assertResult($command->name(), $final->result, "fake {$command->name()} result");
        }
        $this->assertResult('site.workers.status', $fake->call(new SiteWorkersStatus('shop'))->data, 'fake site.workers.status');
        $this->assertResult('site.workers.status', $fake->call(new SiteWorkersStatus('none'))->data, 'fake site.workers.status without workers');
        $metrics = $fake->call(new SystemMetrics)->data;
        $this->assertSame(['horizon', 'queue'], array_column($metrics['workers'], 'worker'));
        $this->assertResult('system.metrics', $metrics, 'fake system.metrics with workers');
    }

    private const S3 = ['endpoint' => 'https://s3.example.com:9000', 'region' => 'us-east-1', 'bucket' => 'zimam-backups', 'prefix' => 'servers/one', 'access_key_id' => 'AKIAEXAMPLE', 'secret_access_key' => 'c2VjcmV0/key+value='];

    /**
     * The backup commands with the least they take, and the fake's answers
     * for all ten: an SFTP destination gets a public key and a pinned host
     * key, a run lists its parts, a restore names what it wrote.
     */
    public function testBackupParamsAndFakeResultsMatchSchemas(): void
    {
        $minimal = [
            new BackupDestination('offsite', 'sftp', ['host' => 'backup.example.com', 'port' => 22, 'user' => 'zimam']),
            new BackupDestination('offsite', 's3', ['endpoint' => 'https://s3.example.com', 'region' => 'auto', 'bucket' => 'b-1', 'access_key_id' => 'k', 'secret_access_key' => 's']),
            new BackupPlan('only-panel', 'offsite', [], [], true, 'hourly', ['keep_last' => 1]),
            new BackupRuns,
            new BackupRestore('nightly', '20261123T020000Z', 'files-shop', 'shop'),
        ];
        foreach ($minimal as $command) {
            $this->assertValid(self::wire($command->jsonParams()), file_get_contents($this->schemaPath($command->name().'.params')), "minimal {$command->name()}");
        }
        $this->assertSame(['id' => 'only-panel', 'destination' => 'offsite', 'panel' => true, 'schedule' => 'hourly', 'retention' => ['keep_last' => 1]], $minimal[2]->params());

        $fake = (new FakeAgentClient)->installMysql()->database('shop', 's_shop_main');
        $jobs = [
            new BackupDestination('offsite', 'sftp', ['host' => 'backup.example.com', 'port' => 22, 'user' => 'zimam'], str_repeat('a', 43), str_repeat('b', 43)),
            new BackupDestinationTest('offsite'),
            new BackupPlan('nightly', 'offsite', ['shop'], ['s_shop_main'], true, 'daily 02:00', ['keep_last' => 7]),
            new BackupRun('nightly'),
        ];
        $results = [];
        foreach ($jobs as $command) {
            $final = $fake->follow($fake->submit($command)->id, 0, function (): void {});
            $this->assertSame('succeeded', $final->status, $command->name());
            $this->assertResult($command->name(), $final->result, "fake {$command->name()} result");
            $results[$command->name()] = $final->result;
        }
        $this->assertStringStartsWith('ssh-ed25519 ', $results['backup.destination']['public_key']);
        $this->assertMatchesRegularExpression('/^SHA256:[A-Za-z0-9+\/]{43}\z/', $results['backup.destination.test']['host_key_fingerprint']);
        $this->assertSame(['files-shop', 'db-s_shop_main', 'panel'], array_column($results['backup.run']['parts'], 'part'));

        $keys = $fake->call(new BackupDestinationKeys('offsite'))->data;
        $this->assertResult('backup.destination.keys', $keys, 'fake backup.destination.keys');
        $this->assertSame(str_repeat('a', 43), $keys['encryption_password']);
        $this->assertResult('backup.runs', $fake->call(new BackupRuns)->data, 'fake backup.runs');
        $this->assertSame([], $fake->call(new BackupRuns('nightly', $results['backup.run']['backup']))->data['runs']);
        $this->assertResult('backup.runs', ['runs' => [['plan' => 'nightly', 'backup' => '20261123T020000Z', 'trigger' => 'timer', 'status' => 'running', 'parts' => [], 'started_at' => '2026-11-23T02:00:00Z', 'finished_at' => null]]], 'a running record');
        $this->assertResult('backup.list', $fake->call(new BackupList('nightly'))->data, 'fake backup.list');

        $backup = $results['backup.run']['backup'];
        foreach ([new BackupRestore('nightly', $backup, 'db-s_shop_main', 'shop', 's_shop_new'), new BackupRestore('nightly', $backup, 'files-shop', 'shop')] as $command) {
            $final = $fake->follow($fake->submit($command)->id, 0, function (): void {});
            $this->assertResult('backup.restore', $final->result, 'fake backup.restore result');
        }
        $this->assertResult('backup.restore', ['site' => 'shop', 'part' => 'files-shop', 'folder' => '/home/s_shop/restores/x', 'skipped_links' => ['site/shared/public/storage']], 'restore with skipped links');
        $this->assertContains('s_shop_new', $fake->databaseNames());

        foreach ([new BackupPlanDelete('nightly'), new BackupDestinationDelete('offsite')] as $command) {
            $final = $fake->follow($fake->submit($command)->id, 0, function (): void {});
            $this->assertResult($command->name(), $final->result, "fake {$command->name()} result");
        }
    }

    /** The secret params are redacted in what the panel records. */
    public function testBackupDestinationRedactsItsSecrets(): void
    {
        $params = (new BackupDestination('offsite', 's3', self::S3, str_repeat('a', 43), str_repeat('b', 43)))->redactedParams();
        $json = json_encode($params);
        foreach ([self::S3['access_key_id'], self::S3['secret_access_key'], str_repeat('a', 43), str_repeat('b', 43)] as $secret) {
            $this->assertStringNotContainsString($secret, $json);
        }
        $this->assertSame('zimam-backups', $params['s3']['bucket']);
    }

    /** A command class missing from commands() would skip every check here. */
    public function testEveryCommandClassIsChecked(): void
    {
        $checked = array_map(fn (array $c): string => $c[0]::class, self::commands());
        foreach (glob(app_path('Agent/Commands/*.php')) as $file) {
            $class = 'App\\Agent\\Commands\\'.basename($file, '.php');
            // The abstract base of the commands that set a service user's password sends nothing itself.
            if ((new \ReflectionClass($class))->isAbstract()) {
                continue;
            }
            $this->assertContains($class, $checked);
        }
    }

    /** Params as they go on the wire validate against <command>.params.json. */
    #[DataProvider('commands')]
    public function testEveryCommandParamsMatchSchema(AgentCommand $command): void
    {
        $schema = $this->schemaPath($command->name().'.params');
        if ($command->e2eOnly()) {
            // Release builds don't know e2e commands, so none is generated.
            $this->assertFileDoesNotExist($schema);

            return;
        }
        $this->assertValid(self::wire($command->jsonParams()), file_get_contents($schema), $command->name().' params');
    }

    /**
     * commands() holds one instance per command, so a static site's params,
     * which leave php_version out, are checked here.
     */
    public function testServerSecurityCheckParamsMatchSchema(): void
    {
        $this->assertValid(self::wire((new SecurityCheck)->jsonParams()), file_get_contents($this->schemaPath('security.check.params')), 'server security.check params');
    }

    public function testStaticSiteProvisionParamsMatchSchema(): void
    {
        $static = new SiteProvision('docs', 'docs.example.com', 'static');
        $this->assertArrayNotHasKey('php_version', $static->params());
        $this->assertValid(self::wire($static->jsonParams()), file_get_contents($this->schemaPath('site.provision.params')), 'static site.provision params');
    }

    /** The optional deploy params are left out, not sent as null or empty: script has no null branch. */
    public function testMinimalDeployParamsMatchSchema(): void
    {
        $deploy = new SiteDeploy('shop', 'webhook', composer: false, keepReleases: 1);
        $this->assertSame(['name' => 'shop', 'composer' => false, 'trigger' => 'webhook', 'keep_releases' => 1], $deploy->params());
        $this->assertValid(self::wire($deploy->jsonParams()), file_get_contents($this->schemaPath('site.deploy.params')), 'minimal site.deploy params');
        $repo = new SiteRepo('shop', 'octo/shop', 'main');
        $this->assertSame(['name' => 'shop', 'repository' => 'octo/shop', 'branch' => 'main'], $repo->params());
        $this->assertValid(self::wire($repo->jsonParams()), file_get_contents($this->schemaPath('site.repo.params')), 'site.repo params without rotate_key');
    }

    /** What a failed site.destroy's result may carry, and what the panel reads from it, match the schema. */
    public function testSiteDestroyResultWithRemovedMatchesSchema(): void
    {
        $schema = file_get_contents($this->schemaPath('site.destroy.result'));
        $this->assertValid(self::wire(['name' => 'shop']), $schema, 'site.destroy result without removed');
        $this->assertValid(self::wire(['name' => 'shop', 'removed' => ['mysql_user' => false, 'valkey_user' => true]]), $schema, 'site.destroy result with removed');
    }

    /** Optional params are left out when unset; the fake's database answers match the result schemas. */
    public function testDatabaseParamsAndFakeResultsMatchSchemas(): void
    {
        $this->assertSame(['name' => 'shop'], (new SiteDestroy('shop'))->params());
        $destroy = new SiteDestroy('shop', dropDatabases: true);
        $this->assertSame(['name' => 'shop', 'drop_databases' => true], $destroy->params());
        $this->assertValid(self::wire($destroy->jsonParams()), file_get_contents($this->schemaPath('site.destroy.params')), 'site.destroy params with drop_databases');
        $this->assertSame([], (new DbList)->params());
        $this->assertValid(self::wire((new DbList('shop'))->jsonParams()), file_get_contents($this->schemaPath('db.list.params')), 'db.list params for a site');

        $fake = (new FakeAgentClient)->installMysql();
        $this->assertResult('db.list', $fake->call(new DbList)->data, 'empty db.list');
        foreach ([new DbCreate('shop', 's_shop_main'), new DbUser('shop', DbUser::generatePassword()), new DbDrop('shop', 's_shop_main')] as $command) {
            $final = $fake->follow($fake->submit($command)->id, 0, function (): void {});
            $this->assertSame('succeeded', $final->status);
            $this->assertResult($command->name(), $final->result, "fake {$command->name()} result");
        }
        $bare = new FakeAgentClient;
        $final = $bare->follow($bare->submit(new MysqlInstall)->id, 0, function (): void {});
        $this->assertSame('succeeded', $final->status);
        $this->assertResult('mysql.install', $final->result, 'fake mysql.install result');

        $valkey = (new FakeAgentClient)->installValkey();
        $final = $valkey->follow($valkey->submit(new CacheUser('shop', CacheUser::generatePassword()))->id, 0, function (): void {});
        $this->assertSame('succeeded', $final->status);
        $this->assertResult('cache.user', $final->result, 'fake cache.user result');
        $final = $bare->follow($bare->submit(new ValkeyInstall)->id, 0, function (): void {});
        $this->assertSame('succeeded', $final->status);
        $this->assertResult('valkey.install', $final->result, 'fake valkey.install result');

        $node = new FakeAgentClient;
        $final = $node->follow($node->submit(new NodeInstall('24'))->id, 0, function (): void {});
        $this->assertResult('node.install', $final->result, 'fake node.install result');
        $this->assertResult('system.status', $node->call(new SystemStatus)->data, 'system.status with Node installed');
        $final = $node->follow($node->submit(new NodeUninstall)->id, 0, function (): void {});
        // Its result is the empty object {}, which PHP decodes as [] (assertResult would read it as a list).
        $this->assertSame([], $final->result);

        $fake->database('shop', 's_shop_main', 4096);
        $this->assertResult('db.list', $fake->call(new DbList('shop'))->data, 'db.list with a database');
    }

    /**
     * The fake's deploy commands answer as the agent's do: the first deploy
     * leaves previous_release out, the next names it (Task 5), and
     * site.releases always has its three lists (Task 6).
     */
    public function testFakeDeployResultsMatchSchemas(): void
    {
        $fake = (new FakeAgentClient)->connectRepository('shop');
        $empty = $fake->call(new SiteReleases('docs'))->data;
        $this->assertSame(['name' => 'docs', 'releases' => [], 'missing' => [], 'unlisted' => []], $empty);
        $this->assertResult('site.releases', $empty, 'fake site.releases without a repository');
        $results = [];
        foreach ([new SiteRepo('shop', 'octo/shop', 'main'), new SiteDeploy('shop', 'manual', true, 5), new SiteDeploy('shop', 'url', true, 5, 'echo hi', str_repeat('b', 40))] as $command) {
            $final = $fake->follow($fake->submit($command)->id, 0, function (): void {});
            $this->assertSame('succeeded', $final->status);
            $this->assertResult($command->name(), $final->result, "fake {$command->name()} result");
            $results[] = $final->result;
        }
        $this->assertArrayNotHasKey('previous_release', $results[1]);
        $this->assertSame($results[1]['release'], $results[2]['previous_release']);
        $this->assertSame(str_repeat('b', 40), $results[2]['commit']);

        $releases = $fake->call(new SiteReleases('shop'))->data;
        $this->assertResult('site.releases', $releases, 'fake site.releases');
        $this->assertSame([$results[2]['release'], $results[1]['release']], array_column($releases['releases'], 'id'));
        $this->assertSame([true, false], array_column($releases['releases'], 'active'));

        $rollback = $fake->follow($fake->submit(new SiteRollback('shop', $results[1]['release']))->id, 0, function (): void {})->result;
        $this->assertResult('site.rollback', $rollback, 'fake site.rollback result');
        $this->assertSame([false, true], array_column($fake->call(new SiteReleases('shop'))->data['releases'], 'active'));
    }

    /** The fake's .env commands answer as the agent's do: not_found until a write makes the file. */
    public function testFakeEnvResultsMatchSchemas(): void
    {
        $fake = new FakeAgentClient;
        try {
            $fake->call(new SiteEnvRead('shop'));
            $this->fail('site.env.read found a .env the site has no');
        } catch (AgentNotFound) {
        }
        $final = $fake->follow($fake->submit(new SiteEnvWrite('shop', "APP_KEY=x\n"))->id, 0, function (): void {});
        $this->assertSame('succeeded', $final->status);
        $this->assertResult('site.env.write', $final->result, 'fake site.env.write result');
        $read = $fake->call(new SiteEnvRead('shop'))->data;
        $this->assertResult('site.env.read', $read, 'fake site.env.read');
        $this->assertSame(['content' => "APP_KEY=x\n"], $read);
    }

    /** The server's zone (ruling R7) is in the fake's status, and the schema takes it. */
    public function testStatusReportsTimezone(): void
    {
        $data = (new FakeAgentClient)->call(new SystemStatus)->data;
        $this->assertSame('UTC', $data['timezone']);
        $this->assertResult('system.status', $data, 'system.status with timezone');
    }

    /** A service the agent couldn't read is left out of system.status: the schema allows it, for either service. */
    public function testStatusMayLeaveAServiceOut(): void
    {
        $fake = new FakeAgentClient;
        foreach ([new \stdClass, ['mysql' => null], ['valkey' => ['installed' => false, 'version' => '', 'active' => false]]] as $services) {
            $data = $fake->call(new SystemStatus)->data;
            $data['services'] = $services;
            $this->assertResult('system.status', $data, 'system.status with services '.json_encode($services));
        }
    }

    /** The guarded commands (R06-D1): their results, firewall.status, and Keep and Revert now. */
    public function testFakeGuardedChangeResultsMatchSchemas(): void
    {
        $fake = (new FakeAgentClient)->firewall(
            [['id' => 'app', 'action' => 'allow', 'proto' => 'tcp', 'port_from' => 8080, 'port_to' => 8080, 'from' => ['203.0.113.0/24']]],
            [['action' => 'allow', 'proto' => 'udp', 'port' => '51820', 'from' => 'any', 'ipv6' => false]],
        );
        $this->assertResult('firewall.status', $fake->call(new FirewallStatus)->data, 'fake firewall.status');
        $changes = [];
        foreach ([new FirewallRules([]), new SshSettings(false, 'no', [22, 2222], 3, 30)] as $command) {
            $final = $fake->follow($fake->submit($command)->id, 0, function (): void {});
            $this->assertSame('succeeded', $final->status);
            $this->assertResult($command->name(), $final->result, "fake {$command->name()} result");
            $changes[] = $final->result['change'];
        }
        $this->assertSame([], $fake->call(new FirewallStatus)->data['rules']);
        $this->assertResult('change.revert', $fake->call(new ChangeRevert($changes[0]))->data, 'fake change.revert');
        $this->assertSame('app', $fake->call(new FirewallStatus)->data['rules'][0]['id']);
        $this->assertResult('change.confirm', $fake->call(new ChangeConfirm($changes[1]))->data, 'fake change.confirm');
        $this->expectException(AgentNotFound::class);
        $fake->call(new ChangeConfirm($changes[1]));
    }

    /** @return list<array<string, mixed>> the three jails as Fail2banForm builds them */
    private static function fail2banJails(): array
    {
        return [
            ['name' => 'sshd', 'enabled' => true, 'max_retry' => 5, 'find_time' => 600, 'ban_time' => 3600],
            ['name' => 'zimam-panel', 'enabled' => true, 'max_retry' => 5, 'find_time' => 600, 'ban_time' => 3600],
            ['name' => 'zimam-site-auth', 'enabled' => false, 'max_retry' => 10, 'find_time' => 600, 'ban_time' => 600],
        ];
    }

    /** fail2ban and Cloudflare mode: status before and after, unban, and both shapes of cloudflare.mode's result. */
    public function testFakeFail2banAndCloudflareResultsMatchSchemas(): void
    {
        $fake = new FakeAgentClient;
        $this->assertResult('fail2ban.status', $fake->call(new Fail2banStatus)->data, 'fake fail2ban.status, not installed');
        $fake->fail2banBan('zimam-panel', '192.0.2.44');
        $this->assertResult('fail2ban.status', $fake->call(new Fail2banStatus)->data, 'fake fail2ban.status with a ban');
        foreach ([new Fail2banConfig(self::fail2banJails(), []), new Fail2banUnban('zimam-panel', '192.0.2.44'), new CloudflareMode(true, false), new CloudflareMode(true, true)] as $command) {
            $final = $fake->follow($fake->submit($command)->id, 0, function (): void {});
            $this->assertSame('succeeded', $final->status);
            $this->assertResult($command->name(), $final->result, "fake {$command->name()} result");
        }
        $this->assertSame([], $fake->call(new Fail2banStatus)->data['jails'][1]['banned']);
        $this->assertArrayHasKey('change', $final->result);
    }

    /** OS updates and reboots, and system.status with automatic updates and a scheduled reboot (R06-D12..D16). */
    public function testFakeUpdateResultsMatchSchemas(): void
    {
        $fake = new FakeAgentClient;
        foreach ([new UpdatesAuto(false, false), new UpdatesAuto(true, true, '04:00'), new UpdatesInstall('security'), new SystemReboot('now'), new SystemReboot('03:30'), new SystemRebootCancel] as $command) {
            $final = $fake->follow($fake->submit($command)->id, 0, function (): void {});
            $this->assertSame('succeeded', $final->status);
            $this->assertResult($command->name(), $final->result, "fake {$command->name()} result");
            if ($command instanceof SystemReboot && $command->at !== 'now') {
                $this->assertResult('system.status', $fake->call(new SystemStatus)->data, 'system.status with a scheduled reboot');
            }
        }
        $this->assertSame(['enabled' => true, 'auto_reboot' => true, 'reboot_time' => '04:00'], $fake->call(new SystemStatus)->data['auto_updates']);
        $this->assertParamsValid(new UpdatesAuto(false, false));
    }

    private function assertParamsValid(AgentCommand $command): void
    {
        $this->assertValid(self::wire($command->jsonParams()), file_get_contents($this->schemaPath($command->name().'.params')), $command->name());
    }

    /** What the fake's jobs answer when they succeed matches each command's result schema. */
    public function testFakeJobResultsMatchSchemas(): void
    {
        $fake = new FakeAgentClient;
        foreach ([new PhpInstall('8.4'), new SiteProvision('shop', 'shop.example.com', 'static'), new SecurityCheck, new SecurityCheck('shop'), new LogsPolicy(30, 730, 2 * 1024 ** 3)] as $command) {
            $job = $fake->submit($command);
            $final = $fake->follow($job->id, 0, function (): void {});
            $this->assertSame('succeeded', $final->status);
            $this->assertResult($command->name(), $final->result, "fake {$command->name()} result");
        }
    }

    public function testFakeResponsesMatchResultAndEnvelopeSchemas(): void
    {
        foreach (['default' => new FakeAgentClient, 'demo' => FakeAgentClient::demo()] as $which => $fake) {
            foreach ([new AgentVersion, new SystemStatus, new SystemMetrics, new LogsList('file.zimam-audit'), new LogsList('journal.ssh'), new LogsRead('file.zimam-audit'), new LogsRead('journal.ssh'), new LogsRead('file.zimam-audit', search: 'line')] as $command) {
                $this->assertResult($command->name(), $fake->call($command)->data, "{$which} {$command->name()}");
            }

            $job = $fake->submit(new E2eSleep(2, 'schema'));
            $this->assertEnvelope(['type' => 'accepted', 'job' => $job->id], "{$which} accepted");
            $this->assertResult('job.status', $fake->status($job->id)->data, "{$which} job.status");
            $final = $fake->follow($job->id, 0, function (string $line, int $offset) use ($job, $which): void {
                $this->assertEnvelope(['type' => 'log', 'job' => $job->id, 'offset' => $offset, 'line' => $line], "{$which} log");
            });
            $this->assertResult('job.follow', $final->data, "{$which} job.follow");

            $other = $fake->submit(new E2eSleep(30, 'schema-cancel'));
            $this->assertResult('job.cancel', $fake->call(new JobCancel($other->id))->data, "{$which} job.cancel");

            foreach (['01JBX7Q3A1B2C3D4E5F6G7H8J9' => AgentNotFound::class, 'bad' => AgentValidationFailed::class] as $id => $class) {
                try {
                    $fake->status($id);
                    $this->fail("{$which}: status({$id}) didn't throw");
                } catch (AgentException $e) {
                    $this->assertInstanceOf($class, $e);
                    $line = ['type' => 'error', 'code' => $e->agentCode(), 'message' => $e->getMessage()];
                    if ($e instanceof AgentValidationFailed) {
                        $line['fields'] = $e->fields();
                    }
                    $this->assertEnvelope($line, "{$which} error {$e->agentCode()}");
                }
            }
        }
    }

    /** secretFields() names exactly the fields the agent marks x-zimam-secret. */
    #[DataProvider('commands')]
    public function testSecretFieldsMatchSchema(AgentCommand $command): void
    {
        if ($command->e2eOnly()) {
            $this->assertSame([], $command->secretFields());

            return;
        }
        $schema = json_decode(file_get_contents($this->schemaPath($command->name().'.params')));
        $this->assertEqualsCanonicalizing(self::secretPaths($schema), $command->secretFields());
    }

    /** The walker above finds secrets at the top level and inside lists. */
    public function testSecretWalkerFindsNestedSecrets(): void
    {
        $schema = json_decode(<<<'JSON'
            {"type": "object", "properties": {
                "name": {"type": "string"},
                "token": {"type": "string", "x-zimam-secret": true},
                "users": {"anyOf": [{"type": "null"}, {"type": "array", "items": {"type": "object", "properties": {
                    "username": {"type": "string"},
                    "password_hash": {"type": "string", "x-zimam-secret": true}
                }}}]}
            }}
            JSON);

        $this->assertEqualsCanonicalizing(['token', 'users.*.password_hash'], self::secretPaths($schema));
        $this->assertEqualsCanonicalizing(self::secretPaths($schema), (new SecretCommand)->secretFields());
    }

    #[DataProvider('commands')]
    public function testRuntimeMaxMatchesDesign(AgentCommand $command): void
    {
        if ($command->kind() !== AgentCommandKind::Job) {
            $this->assertNull($command->runtimeMaxSeconds());

            return;
        }
        $limits = self::agentRuntimeLimits();
        $this->assertArrayHasKey($command->name(), $limits, 'a job command the agent has no limits for');
        $this->assertSame($limits[$command->name()], $command->runtimeMaxSeconds());
    }

    /**
     * The schemas' patterns end in $, which PCRE lets match before a final
     * newline unless the D modifier is set. opis sets it; this keeps it so.
     */
    public function testPatternsRejectTrailingNewline(): void
    {
        $schema = file_get_contents($this->schemaPath('job.status.params'));
        $this->assertTrue((new Validator)->validate(self::wire(['job' => self::JOB]), $schema)->isValid());
        $this->assertFalse((new Validator)->validate(self::wire(['job' => self::JOB."\n"]), $schema)->isValid());
    }

    private function schemaPath(string $name): string
    {
        return base_path("../contract/schema/{$name}.json");
    }

    /** @param array<string, mixed> $data */
    private function assertResult(string $command, array $data, string $what): void
    {
        $this->assertValid(self::wire($data), file_get_contents($this->schemaPath("{$command}.result")), "{$what} data");
        $this->assertEnvelope(['type' => 'result', 'ok' => true, 'data' => $data], "{$what} result line");
    }

    /** @param array<string, mixed> $line */
    private function assertEnvelope(array $line, string $what): void
    {
        $this->assertValid(self::wire($line), file_get_contents($this->schemaPath('envelope')), $what);
    }

    private function assertValid(mixed $data, string $schema, string $what): void
    {
        $result = (new Validator)->validate($data, $schema);
        $this->assertTrue($result->isValid(), "{$what}: ".json_encode($result->hasError() ? (new ErrorFormatter)->format($result->error()) : []));
    }

    /** The value as the agent decodes it: JSON objects stay objects. */
    private static function wire(mixed $value): mixed
    {
        return json_decode(json_encode($value, JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES), false, 512, JSON_THROW_ON_ERROR);
    }

    /** @return list<string> the dotted paths, "*" for list items, of every x-zimam-secret field */
    private static function secretPaths(object $schema, string $prefix = ''): array
    {
        $paths = [];
        if (($schema->{'x-zimam-secret'} ?? false) === true) {
            $paths[] = rtrim($prefix, '.');
        }
        foreach ((array) ($schema->properties ?? []) as $name => $property) {
            array_push($paths, ...self::secretPaths($property, "{$prefix}{$name}."));
        }
        if (isset($schema->items)) {
            array_push($paths, ...self::secretPaths($schema->items, "{$prefix}*."));
        }
        foreach (['anyOf', 'oneOf', 'allOf'] as $keyword) {
            foreach ($schema->{$keyword} ?? [] as $branch) {
                array_push($paths, ...self::secretPaths($branch, $prefix));
            }
        }

        return $paths;
    }
}
