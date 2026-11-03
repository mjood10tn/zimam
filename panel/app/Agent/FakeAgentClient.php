<?php

namespace App\Agent;

use App\Agent\Commands\JobCancel;
use App\Agent\Commands\JobFollow;
use App\Agent\Commands\JobStatus;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentConflict;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentJobFailed;
use App\Agent\Exceptions\AgentNotFound;
use App\Agent\Exceptions\AgentUnknownCommand;
use App\Agent\Exceptions\AgentValidationFailed;
use Carbon\CarbonImmutable;
use Closure;
use Illuminate\Support\Str;
use LogicException;

/**
 * Stands in for the agent in tests and under Herd (design §6, §14.6). It
 * answers from memory as the agent would, and keeps the agent's rules for
 * job ids, busy subjects and the cap of two unfinished jobs (§5.7).
 *
 * A job is queued when submitted. Each status() call moves it one state on,
 * through running to succeeded unless scriptJob() set other states, so a job
 * ends without ever being followed, as TrackAgentJob needs; follow() streams
 * its log and ends it at once. respond() scripts answers per command.
 */
final class FakeAgentClient implements AgentClient
{
    private const JOB_ID = '/^[0-9A-HJKMNP-TV-Z]{26}\z/';

    private const SITE_NAME = '/^[a-z][a-z0-9-]{0,22}[a-z0-9]\z/';

    private const RESERVED_NAMES = ['zimam', 'panel', 'default', 'acme'];

    private const DOMAIN = '/^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+([a-z]{1,63}|xn--[a-z0-9-]{0,58}[a-z0-9])\z/';

    private const MAX_UNFINISHED = 2;

    /** @var list<array{method: string, command: string, params: array<string, mixed>}> */
    private array $calls = [];

    /** @var array<string, list<array<string, mixed>|AgentException|Closure>> */
    private array $answers = [];

    /** @var array<string, array{states: list<string>, log: list<string>, result: mixed, error_code: ?string, message: string}> */
    private array $scripts = [];

    /** @var array<string, array<string, mixed>> */
    private array $jobs = [];

    /** @var array<string, mixed> */
    private array $systemStatus;

    public function __construct()
    {
        $this->systemStatus = self::serverStatus(demo: false);
    }

    /** A fake with a server worth looking at under Herd: several PHP versions, pending updates, a reboot due. */
    public static function demo(): self
    {
        $fake = new self;
        $fake->systemStatus = self::serverStatus(demo: true);

        return $fake;
    }

    /**
     * Scripts what the agent answers $command: the answers in order, the last
     * one repeating. An array is the result's data (for a job command, the
     * accepted job's {"job": id}), an exception is thrown, and a closure gets
     * the command and returns or throws one of those.
     */
    public function respond(string $command, array|AgentException|Closure ...$answers): static
    {
        $this->answers[$command] = array_values($answers);

        return $this;
    }

    /**
     * Scripts the jobs $command submits from now on: each status() call
     * reports the next of $states, and the last one stays.
     *
     * @param  list<string>  $states
     * @param  list<string>  $log  the job's log messages, without timestamps
     */
    public function scriptJob(string $command, array $states, array $log = [], mixed $result = null, ?string $errorCode = null, string $message = ''): static
    {
        $this->scripts[$command] = ['states' => $states, 'log' => $log, 'result' => $result, 'error_code' => $errorCode, 'message' => $message];

        return $this;
    }

    /**
     * Changes the server the fake reports in system.status, and checks the
     * PHP commands against: $change gets system.status's data and returns it.
     *
     * @param  Closure(array<string, mixed> $status): array<string, mixed>  $change
     */
    public function changeServer(Closure $change): static
    {
        $this->systemStatus = $change($this->systemStatus);

        return $this;
    }

    /** @return list<array{method: string, command: string, params: array<string, mixed>}> */
    public function calls(): array
    {
        return $this->calls;
    }

    public function call(AgentCommand $command): AgentResult
    {
        $this->record('call', $command);
        if (($answer = $this->answer($command)) !== null) {
            return new AgentResult($answer);
        }

        return new AgentResult(match (true) {
            $command->name() === 'agent.version' => ['version' => '0.1.0', 'protocols' => [1], 'build' => 'release'],
            $command->name() === 'system.status' => $this->systemStatus,
            $command instanceof JobStatus => $this->advance($command->job),
            $command instanceof JobCancel => $this->cancel($command->job),
            $command instanceof JobFollow => throw new LogicException('job.follow streams: use follow()'),
            default => throw new AgentUnknownCommand("Unknown command \"{$command->name()}\""),
        });
    }

    public function submit(AgentCommand $command): AgentJob
    {
        $this->record('submit', $command);
        if (($answer = $this->answer($command)) !== null) {
            return new AgentJob($answer['job']);
        }
        if ($command->kind() !== AgentCommandKind::Job) {
            throw new LogicException("{$command->name()} isn't a job");
        }
        $this->validate($command);
        $subject = self::subject($command);
        $unfinished = array_filter($this->jobs, fn (array $job): bool => ! in_array($job['status'], AgentJobStatus::FINISHED, true));
        if (in_array($subject, array_column($unfinished, 'subject'), true)) {
            throw new AgentBusy("{$subject} already has a job running");
        }
        if (count($unfinished) >= self::MAX_UNFINISHED) {
            throw new AgentBusy('Two jobs are already running');
        }
        $this->precheck($command);

        return new AgentJob($this->queue($command, $subject));
    }

    /** The agent's prechecks for the PHP commands (§8.3–§8.5), against the fake's own server. */
    private function precheck(AgentCommand $command): void
    {
        if (! str_starts_with($command->name(), 'php.')) {
            return;
        }
        $php = $this->systemStatus['php'];
        $version = $command->params()['version'];
        $installed = array_column($php['installed'], null, 'version');
        $available = array_column($php['available'], null, 'version');
        match (true) {
            $command->name() === 'php.install' && ! $available[$version]['supported'] => throw new AgentValidationFailed('Invalid parameters', [
                'version' => "PHP {$version}'s security support ended on {$available[$version]['security_end']}, so it can't be installed",
            ]),
            $command->name() === 'php.install' && isset($installed[$version]) => throw new AgentConflict("PHP {$version} is already installed."),
            $command->name() !== 'php.install' && ! isset($installed[$version]) => throw new AgentNotFound("PHP {$version} isn't installed."),
            $command->name() === 'php.uninstall' && $version === $php['panel'] => throw new AgentConflict("The panel runs on PHP {$version}, so it can't be uninstalled."),
            $command->name() === 'php.uninstall' && $version === $php['default'] => throw new AgentConflict("PHP {$version} is the default version. Make another version the default first."),
            $command->name() === 'php.uninstall' && $installed[$version]['in_use'] => throw new AgentConflict("Sites still run on PHP {$version}. Move them to another version or delete them first."),
            default => null,
        };
    }

    /**
     * What a job command answers when it succeeds, as the agent's result types are (§5.3).
     *
     * @return array<string, mixed>
     */
    private static function defaultResult(AgentCommand $command): array
    {
        $params = $command->params();

        return match ($command->name()) {
            'php.install', 'php.uninstall', 'php.default' => ['version' => $params['version']],
            'site.provision' => ['name' => $params['name'], 'template_version' => 1],
            default => [],
        };
    }

    /**
     * What a succeeded job changed on the fake's server, so system.status
     * reports it: PHP versions, and the version a PHP site's pool now uses.
     *
     * @param  array<string, mixed>  $params
     */
    private function applyEffect(string $command, array $params): void
    {
        $php = &$this->systemStatus['php'];
        match ($command) {
            'php.install' => $php['installed'][] = ['version' => $params['version'], 'fpm_active' => false, 'in_use' => false],
            'php.uninstall' => $php['installed'] = array_values(array_filter($php['installed'], fn (array $v): bool => $v['version'] !== $params['version'])),
            'php.default' => $php['default'] = $params['version'],
            'site.provision' => $php['installed'] = array_map(
                fn (array $v): array => $v['version'] === ($params['php_version'] ?? null) ? ['version' => $v['version'], 'fpm_active' => true, 'in_use' => true] : $v,
                $php['installed'],
            ),
            default => null,
        };
        usort($php['installed'], fn (array $a, array $b): int => version_compare($a['version'], $b['version']));
    }

    public function status(string $jobId): AgentJobStatus
    {
        $command = new JobStatus($jobId);
        $this->record('status', $command);

        return AgentJobStatus::fromArray($this->answer($command) ?? $this->advance($jobId));
    }

    /** @param Closure(string $line, int $offset): void $onLine */
    public function follow(string $jobId, int $fromOffset, Closure $onLine): AgentJobStatus
    {
        $command = new JobFollow($jobId, $fromOffset);
        $this->record('follow', $command);
        if (($answer = $this->answer($command)) !== null) {
            return AgentJobStatus::fromArray($answer);
        }
        if ($fromOffset < 0) {
            throw new AgentValidationFailed('Invalid parameters', ['from_offset' => 'must be 0 or more']);
        }
        $this->find($jobId);
        if (! in_array($this->jobs[$jobId]['status'], AgentJobStatus::FINISHED, true)) {
            $this->moveTo($jobId, end($this->jobs[$jobId]['states']));
        }
        $offset = 0;
        foreach ($this->jobs[$jobId]['log'] as $line) {
            if ($offset >= $fromOffset) {
                $onLine($line, $offset);
            }
            $offset += strlen($line) + 1;
        }
        $data = $this->statusData($jobId);
        if ($data['status'] === 'failed') {
            throw new AgentJobFailed($data['message']);
        }

        return AgentJobStatus::fromArray($data);
    }

    private function record(string $method, AgentCommand $command): void
    {
        $this->calls[] = ['method' => $method, 'command' => $command->name(), 'params' => $command->params()];
    }

    /** @return array<string, mixed>|null the scripted answer's data, or null when nothing is scripted */
    private function answer(AgentCommand $command): ?array
    {
        $answers = $this->answers[$command->name()] ?? [];
        if ($answers === []) {
            return null;
        }
        $answer = count($answers) > 1 ? array_shift($this->answers[$command->name()]) : $answers[0];
        if ($answer instanceof Closure) {
            $answer = $answer($command);
        }
        if ($answer instanceof AgentException) {
            throw $answer;
        }

        return $answer;
    }

    /** The agent's own checks for the commands it knows (§5.4). */
    private function validate(AgentCommand $command): void
    {
        $params = $command->params();
        if (str_starts_with($command->name(), 'php.')) {
            if (! in_array($params['version'], array_column($this->systemStatus['php']['available'], 'version'), true)) {
                throw new AgentValidationFailed('Invalid parameters', ['version' => 'is not a supported PHP version']);
            }

            return;
        }
        if ($command->name() === 'site.provision') {
            $this->validateProvision($params);

            return;
        }
        if ($command->name() !== 'e2e.sleep') {
            return;
        }
        $fields = [];
        if (! is_int($params['seconds']) || $params['seconds'] < 1 || $params['seconds'] > 120) {
            $fields['seconds'] = 'must be between 1 and 120';
        }
        if (! preg_match(self::SITE_NAME, $params['subject']) || in_array($params['subject'], self::RESERVED_NAMES, true)) {
            $fields['subject'] = 'must be 2 to 24 lowercase letters, digits and hyphens, starting with a letter';
        }
        if ($fields !== []) {
            throw new AgentValidationFailed('Invalid parameters', $fields);
        }
    }

    /** @param array<string, mixed> $params site.provision's (§5.3, §5.4) */
    private function validateProvision(array $params): void
    {
        $fields = [];
        if (! preg_match(self::SITE_NAME, $params['name']) || in_array($params['name'], self::RESERVED_NAMES, true)) {
            $fields['name'] = 'must be 2–24 lowercase letters, digits or hyphens, start with a letter and not end with a hyphen';
        }
        if (strlen($params['domain']) > 253 || ! preg_match(self::DOMAIN, $params['domain'])) {
            $fields['domain'] = 'must be lowercase ASCII labels of letters, digits and inner hyphens';
        }
        $php = $params['php_version'] ?? null;
        match (true) {
            ! in_array($params['type'], ['static', 'php'], true) => $fields['type'] = 'must be static or php',
            $params['type'] === 'php' && $php === null => $fields['php_version'] = 'is required for a PHP site',
            $params['type'] === 'php' && ! in_array($php, array_column($this->systemStatus['php']['available'], 'version'), true) => $fields['php_version'] = 'is not a supported PHP version',
            $params['type'] === 'static' && $php !== null => $fields['php_version'] = 'must be left out for a static site',
            default => null,
        };
        if ($fields !== []) {
            throw new AgentValidationFailed('Invalid parameters', $fields);
        }
    }

    private static function subject(AgentCommand $command): string
    {
        $params = $command->params();

        return match (true) {
            $command->name() === 'e2e.sleep' => 'e2e:'.$params['subject'],
            str_starts_with($command->name(), 'site.') || $command->name() === 'cert.issue' => 'site:'.($params['name'] ?? ''),
            str_starts_with($command->name(), 'php.') && $command->name() !== 'php.default' => 'php:'.($params['version'] ?? ''),
            default => 'server',
        };
    }

    private function queue(AgentCommand $command, string $subject): string
    {
        $id = strtoupper((string) Str::ulid());
        $script = $this->scripts[$command->name()] ?? null;
        $now = CarbonImmutable::now('UTC');
        $log = $script['log'] ?? [$command->name() === 'e2e.sleep' ? "Sleeping {$command->params()['seconds']} seconds" : "Running {$command->name()}"];
        $this->jobs[$id] = [
            'job' => $id,
            'command' => $command->name(),
            'params' => $command->params(),
            'subject' => $subject,
            'status' => 'queued',
            'states' => $script['states'] ?? ['running', 'succeeded'],
            'step' => 0,
            'log' => array_map(fn (string $message): string => '['.self::goTime($now).'] '.$message, $log),
            'result' => $script === null ? self::defaultResult($command) : $script['result'],
            'error_code' => $script['error_code'] ?? null,
            'message' => $script['message'] ?? '',
            'started_at' => null,
            'finished_at' => null,
        ];

        return $id;
    }

    /** @return array<string, mixed> job.status's data, after moving the job one state on */
    private function advance(string $jobId): array
    {
        $this->find($jobId);
        $job = $this->jobs[$jobId];
        if (! in_array($job['status'], AgentJobStatus::FINISHED, true)) {
            $this->moveTo($jobId, $job['states'][min($job['step'], count($job['states']) - 1)]);
            $this->jobs[$jobId]['step']++;
        }

        return $this->statusData($jobId);
    }

    /** @return array{status: string} */
    private function cancel(string $jobId): array
    {
        $this->find($jobId);
        if (in_array($this->jobs[$jobId]['status'], AgentJobStatus::FINISHED, true)) {
            throw new AgentConflict('The job has already ended');
        }
        $this->jobs[$jobId]['log'][] = '['.self::goTime(CarbonImmutable::now('UTC')).'] Stopped';
        $this->moveTo($jobId, 'cancelled');

        return ['status' => 'cancelled'];
    }

    private function find(string $jobId): void
    {
        if (! preg_match(self::JOB_ID, $jobId)) {
            throw new AgentValidationFailed('Invalid parameters', ['job' => 'must be a ULID']);
        }
        if (! isset($this->jobs[$jobId])) {
            throw new AgentNotFound("No job {$jobId}");
        }
    }

    private function moveTo(string $jobId, string $status): void
    {
        $now = CarbonImmutable::now('UTC');
        $job = &$this->jobs[$jobId];
        $job['status'] = $status;
        if ($status !== 'queued' && $status !== 'cancelled') {
            $job['started_at'] ??= $now;
        }
        if (in_array($status, AgentJobStatus::FINISHED, true)) {
            $job['finished_at'] = $now;
        }
        if ($status === 'succeeded') {
            $this->applyEffect($job['command'], $job['params']);
        }
    }

    /** @return array<string, mixed> */
    private function statusData(string $jobId): array
    {
        $job = $this->jobs[$jobId];
        $failed = $job['status'] === 'failed';

        return [
            'job' => $job['job'],
            'command' => $job['command'],
            'subject' => $job['subject'],
            'status' => $job['status'],
            'error_code' => $failed ? ($job['error_code'] ?? 'internal') : null,
            'message' => $failed ? $job['message'] : '',
            'result' => $job['status'] === 'succeeded' ? $job['result'] : null,
            'started_at' => $job['started_at'] ? self::goTime($job['started_at']) : null,
            'finished_at' => $job['finished_at'] ? self::goTime($job['finished_at']) : null,
            'log_size' => $job['status'] === 'queued' ? 0 : array_sum(array_map(fn (string $line): int => strlen($line) + 1, $job['log'])),
        ];
    }

    /** RFC 3339 in UTC, as Go writes a time: fractional digits only when needed. */
    private static function goTime(CarbonImmutable $time): string
    {
        $fraction = rtrim($time->format('u'), '0');

        return $time->format('Y-m-d\TH:i:s').($fraction === '' ? '' : '.'.$fraction).'Z';
    }

    /** @return array<string, mixed> system.status's data (§7.5) */
    private static function serverStatus(bool $demo): array
    {
        $today = CarbonImmutable::now('UTC')->startOfDay();
        $available = [];
        foreach (['8.2' => '2026-12-31', '8.3' => '2027-12-31', '8.4' => '2028-12-31', '8.5' => '2029-12-31'] as $version => $end) {
            $available[] = ['version' => $version, 'security_end' => $end, 'supported' => $today->lte(CarbonImmutable::parse($end, 'UTC'))];
        }

        return [
            'agent_version' => '0.1.0',
            'protocols' => [1],
            'nginx_version' => '1.28.3',
            'php' => [
                'installed' => $demo
                    ? [
                        ['version' => '8.3', 'fpm_active' => false, 'in_use' => false],
                        ['version' => '8.4', 'fpm_active' => true, 'in_use' => true],
                        ['version' => '8.5', 'fpm_active' => true, 'in_use' => true],
                    ]
                    // The panel's pool puts its version in use.
                    : [['version' => '8.5', 'fpm_active' => true, 'in_use' => true]],
                'default' => $demo ? '8.4' : '8.5',
                'panel' => '8.5',
                'available' => $available,
            ],
            'reboot_required' => $demo,
            'updates' => [
                'total' => $demo ? 12 : 0,
                'security' => $demo ? 3 : 0,
                'checked_at' => self::goTime(CarbonImmutable::now('UTC')->subMinutes($demo ? 25 : 10)),
            ],
            'panel_domain' => 'panel.example.com',
            'acme_directory' => 'https://acme-v02.api.letsencrypt.org/directory',
        ];
    }
}
