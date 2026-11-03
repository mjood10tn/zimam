<?php

namespace Tests\Unit\Agent;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;
use App\Agent\Commands\AgentVersion;
use App\Agent\Commands\E2eSleep;
use App\Agent\Commands\JobCancel;
use App\Agent\Commands\JobFollow;
use App\Agent\Commands\JobStatus;
use App\Agent\Commands\PhpDefault;
use App\Agent\Commands\PhpInstall;
use App\Agent\Commands\PhpUninstall;
use App\Agent\Commands\SiteProvision;
use App\Agent\Commands\SystemStatus;
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

    /** §5.7's RuntimeMaxSec for every job command, plus e2e.sleep's (D22). */
    private const RUNTIME_MAX = [
        'php.install' => 900,
        'php.uninstall' => 900,
        'site.provision' => 300,
        'site.php' => 300,
        'site.security' => 300,
        'site.destroy' => 300,
        'cert.issue' => 300,
        'security.check' => 600,
        'php.default' => 120,
        'logs.policy' => 120,
        'e2e.sleep' => 180,
    ];

    /** @return array<string, array{AgentCommand}> one of every command the panel sends */
    public static function commands(): array
    {
        $commands = [
            new AgentVersion,
            new SystemStatus,
            new JobStatus(self::JOB),
            new JobFollow(self::JOB, 0),
            new JobCancel(self::JOB),
            new E2eSleep(5, 'shop'),
            new PhpInstall('8.4'),
            new PhpUninstall('8.3'),
            new PhpDefault('8.5'),
            new SiteProvision('shop', 'shop.example.com', 'php', '8.4'),
        ];

        return array_combine(
            array_map(fn (AgentCommand $c): string => $c->name(), $commands),
            array_map(fn (AgentCommand $c): array => [$c], $commands),
        );
    }

    /** A command class missing from commands() would skip every check here. */
    public function testEveryCommandClassIsChecked(): void
    {
        $checked = array_map(fn (array $c): string => $c[0]::class, self::commands());
        foreach (glob(app_path('Agent/Commands/*.php')) as $file) {
            $this->assertContains('App\\Agent\\Commands\\'.basename($file, '.php'), $checked);
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
    public function testStaticSiteProvisionParamsMatchSchema(): void
    {
        $static = new SiteProvision('docs', 'docs.example.com', 'static');
        $this->assertArrayNotHasKey('php_version', $static->params());
        $this->assertValid(self::wire($static->jsonParams()), file_get_contents($this->schemaPath('site.provision.params')), 'static site.provision params');
    }

    /** What the fake's jobs answer when they succeed matches each command's result schema. */
    public function testFakeJobResultsMatchSchemas(): void
    {
        $fake = new FakeAgentClient;
        foreach ([new PhpInstall('8.4'), new SiteProvision('shop', 'shop.example.com', 'static')] as $command) {
            $job = $fake->submit($command);
            $final = $fake->follow($job->id, 0, function (): void {});
            $this->assertSame('succeeded', $final->status);
            $this->assertResult($command->name(), $final->result, "fake {$command->name()} result");
        }
    }

    public function testFakeResponsesMatchResultAndEnvelopeSchemas(): void
    {
        foreach (['default' => new FakeAgentClient, 'demo' => FakeAgentClient::demo()] as $which => $fake) {
            foreach ([new AgentVersion, new SystemStatus] as $command) {
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
        $this->assertArrayHasKey($command->name(), self::RUNTIME_MAX, 'a job command missing from §5.7');
        $this->assertSame(self::RUNTIME_MAX[$command->name()], $command->runtimeMaxSeconds());
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
