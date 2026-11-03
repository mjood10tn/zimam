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
use App\Models\SecurityFinding;
use App\Models\SiteBasicAuthUser;
use App\Support\IpEntry;
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

    /** @var array<string, string> the PHP version of each site's pool, by site name, as succeeded jobs left them */
    private array $pools = [];

    /** How many times system.metrics was answered: its counters move on with each answer. */
    private int $metricsCalls = 0;

    /** @var list<Closure(array<string, mixed>): array<string, mixed>> */
    private array $metricsChanges = [];

    /** @var array<string, true> the sites with a certificate, by name, as succeeded cert.issue jobs left them */
    private array $certified = [];

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

    /** Gives a site a certificate on the fake's server, as a succeeded cert.issue would. */
    public function certify(string $site): static
    {
        $this->certified[$site] = true;

        return $this;
    }

    /**
     * Changes what system.metrics answers from now on: $change gets the data
     * the fake would answer, counters already moved on, and returns it.
     *
     * @param  Closure(array<string, mixed> $metrics): array<string, mixed>  $change
     */
    public function changeMetrics(Closure $change): static
    {
        $this->metricsChanges[] = $change;

        return $this;
    }

    /**
     * A job's log lines as far as it has run, "[<time>] <message>" as the
     * agent writes them; none for a job the fake doesn't know.
     *
     * @return list<string>
     */
    public function logLines(string $jobId): array
    {
        $job = $this->jobs[$jobId] ?? null;

        return $job === null || $job['status'] === 'queued' ? [] : $job['log'];
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
            $command->name() === 'system.metrics' => $this->metrics(),
            $command->name() === 'logs.list' => $this->logsList($command->params()),
            $command->name() === 'logs.read' => $this->logsRead($command->params()),
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

    /**
     * The agent's prechecks for the PHP commands (§8.3–§8.5) and for the
     * version site.php moves to (§9.5), against the fake's own server.
     */
    private function precheck(AgentCommand $command): void
    {
        if ($command->name() === 'site.security' && $command->params()['force_https'] && ! isset($this->certified[$command->params()['name']])) {
            throw new AgentConflict("Site {$command->params()['name']} has no certificate yet, so HTTPS can't be forced. Issue one first.");
        }
        if ($command->name() === 'site.php') {
            $version = $command->params()['php_version'];
            $available = array_column($this->systemStatus['php']['available'], null, 'version');
            match (true) {
                ! in_array($version, array_column($this->systemStatus['php']['installed'], 'version'), true) => throw new AgentNotFound("PHP {$version} isn't installed."),
                ! $available[$version]['supported'] => throw new AgentValidationFailed('Invalid parameters', [
                    'php_version' => "PHP {$version}'s security support ended on {$available[$version]['security_end']}, so sites can't move to it",
                ]),
                ($this->pools[$command->params()['name']] ?? null) === $version => throw new AgentConflict("Site {$command->params()['name']} already runs PHP {$version}."),
                default => null,
            };

            return;
        }
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
            'site.php' => ['name' => $params['name'], 'php_version' => $params['php_version']],
            'site.destroy', 'site.security' => ['name' => $params['name']],
            'security.check' => self::securityFindings($params),
            'logs.policy' => $params,
            // Let's Encrypt's certificates last 90 days.
            'cert.issue' => ['expires_at' => self::goTime(CarbonImmutable::now('UTC')->addDays(90))],
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
        $moved = null;
        if (in_array($command, ['site.provision', 'site.php'], true) && isset($params['php_version'])) {
            $moved = $this->pools[$params['name']] ?? null;
            $this->pools[$params['name']] = $params['php_version'];
        }
        if ($command === 'site.destroy') {
            $moved = $this->pools[$params['name']] ?? null;
            unset($this->pools[$params['name']], $this->certified[$params['name']]);
        }
        if ($command === 'cert.issue') {
            $this->certified[$params['name']] = true;
        }
        match ($command) {
            'php.install' => $php['installed'][] = ['version' => $params['version'], 'fpm_active' => false, 'in_use' => false],
            'php.uninstall' => $php['installed'] = array_values(array_filter($php['installed'], fn (array $v): bool => $v['version'] !== $params['version'])),
            'php.default' => $php['default'] = $params['version'],
            // The new version runs the site's pool; the one it left is in use
            // while the panel or another site still has a pool on it.
            'site.provision', 'site.php', 'site.destroy' => $php['installed'] = array_map(
                fn (array $v): array => match ($v['version']) {
                    $params['php_version'] ?? null => ['version' => $v['version'], 'fpm_active' => true, 'in_use' => true],
                    $moved => ['version' => $v['version'], 'fpm_active' => $v['version'] === $php['panel'] || in_array($moved, $this->pools, true), 'in_use' => $v['version'] === $php['panel'] || in_array($moved, $this->pools, true)],
                    default => $v,
                },
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
        if ($command->name() === 'site.security') {
            $this->validateSecurity($params);

            return;
        }
        if ($command->name() === 'logs.policy') {
            $fields = [];
            if ($params['retention_days'] < 1 || $params['retention_days'] > 365) {
                $fields['retention_days'] = 'must be between 1 and 365';
            }
            if ($params['audit_retention_days'] < 1 || $params['audit_retention_days'] > 3650) {
                $fields['audit_retention_days'] = 'must be between 1 and 3650';
            }
            if ($params['site_cap_bytes'] < 1024 ** 3 || $params['site_cap_bytes'] > 100 * 1024 ** 3) {
                $fields['site_cap_bytes'] = 'must be between 1073741824 and 107374182400 bytes';
            }
            if ($fields !== []) {
                throw new AgentValidationFailed('Invalid parameters', $fields);
            }

            return;
        }
        if ($command->name() === 'security.check') {
            $site = $params['scope'] === 'site';
            if (! in_array($params['scope'], ['server', 'site'], true) || $site !== isset($params['name']) || ($site && ! preg_match(self::SITE_NAME, $params['name']))) {
                throw new AgentValidationFailed('Invalid parameters', ['scope' => 'must be server, or site with a site name']);
            }

            return;
        }
        if (in_array($command->name(), ['site.php', 'site.destroy', 'cert.issue'], true)) {
            $fields = [];
            if (! preg_match(self::SITE_NAME, $params['name']) || in_array($params['name'], self::RESERVED_NAMES, true)) {
                $fields['name'] = 'must be 2–24 lowercase letters, digits or hyphens, start with a letter and not end with a hyphen';
            }
            if ($command->name() === 'site.php' && ! in_array($params['php_version'], array_column($this->systemStatus['php']['available'], 'version'), true)) {
                $fields['php_version'] = 'is not a supported PHP version';
            }
            if ($fields !== []) {
                throw new AgentValidationFailed('Invalid parameters', $fields);
            }

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

    /**
     * security.check's result when every check passes (§9.7).
     *
     * @param  array<string, mixed>  $params
     * @return array<string, mixed>
     */
    private static function securityFindings(array $params): array
    {
        $checks = ($params['scope'] ?? 'server') === 'site' ? SecurityFinding::SITE_CHECKS : SecurityFinding::SERVER_CHECKS;
        $result = ['scope' => $params['scope']];
        if (isset($params['name'])) {
            $result['name'] = $params['name'];
        }
        $result['findings'] = array_map(fn (string $check): array => [
            'check' => $check,
            'status' => 'pass',
            'detail' => 'Fine.',
            'expires_at' => null,
        ], $checks);

        return $result;
    }

    /** @param array<string, mixed> $params site.security's (§5.4, §9.6) */
    private function validateSecurity(array $params): void
    {
        $fields = [];
        if (! preg_match(self::SITE_NAME, $params['name']) || in_array($params['name'], self::RESERVED_NAMES, true)) {
            $fields['name'] = 'must be 2–24 lowercase letters, digits or hyphens, start with a letter and not end with a hyphen';
        }
        if ($params['hsts']['max_age'] !== null && ! in_array($params['hsts']['max_age'], [86400, 2592000, 31536000], true)) {
            $fields['hsts.max_age'] = 'must be 86400, 2592000 or 31536000';
        }
        foreach (['ip_allow', 'ip_deny'] as $list) {
            if (count($params[$list]) > IpEntry::MAX_ENTRIES) {
                $fields[$list] = 'must have at most 100 entries';
            }
            foreach ($params[$list] as $i => $entry) {
                if (IpEntry::error($entry) !== null) {
                    $fields[$list] ??= 'entry '.($i + 1).' '.IpEntry::error($entry);
                }
            }
        }
        $users = $params['password_protection']['users'];
        $names = array_column($users, 'username');
        foreach ($users as $i => $user) {
            match (true) {
                ! preg_match(SiteBasicAuthUser::USERNAME_PATTERN, $user['username']) => $fields['password_protection.users'] ??= 'user '.($i + 1).': username must be 1–32 letters, digits, dots, underscores or hyphens',
                ! preg_match('/^\$2y\$1[0-4]\$[.\/A-Za-z0-9]{53}\z/', $user['password_hash']) => $fields['password_protection.users'] ??= 'user '.($i + 1).': password hash must be bcrypt ($2y$, cost 10–14)',
                default => null,
            };
        }
        if (count($users) > SiteBasicAuthUser::MAX_USERS || count(array_unique($names)) !== count($names)) {
            $fields['password_protection.users'] ??= 'must have at most 20 users, each username once';
        }
        if ($params['password_protection']['enabled'] && $users === []) {
            $fields['password_protection.users'] = 'needs at least one user while password protection is on';
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
            $command->name() === 'security.check' => isset($params['name']) ? 'site:'.$params['name'] : 'server',
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

    /**
     * system.metrics's data (§7.5): a healthy two-CPU server whose counters
     * move on by five seconds' worth with each answer, about 15% busy and
     * 50 kB/s in, with FPM monitored for each version in use.
     *
     * @return array<string, mixed>
     */
    private function metrics(): array
    {
        $n = $this->metricsCalls++;
        $gib = 1024 ** 3;
        $services = [['unit' => 'nginx.service', 'active_state' => 'active', 'unit_file_state' => 'enabled']];
        foreach ($this->systemStatus['php']['installed'] as $php) {
            if ($php['in_use']) {
                $services[] = ['unit' => "php{$php['version']}-fpm.service", 'active_state' => 'active', 'unit_file_state' => 'enabled'];
            }
        }
        foreach (['zimam-agent.socket', 'zimam-panel-queue.service', 'zimam-panel-scheduler.timer', 'zimam-maintenance.timer', 'certbot.timer', 'ssh.socket'] as $unit) {
            $services[] = ['unit' => $unit, 'active_state' => 'active', 'unit_file_state' => 'enabled'];
        }
        $metrics = [
            'uptime_seconds' => 1021453.67 + 5 * $n,
            'load' => ['load1' => 0.42, 'load5' => 0.31, 'load15' => 0.27],
            'cpu' => [
                'user' => 1254871 + 110 * $n,
                'nice' => 3021,
                'system' => 402987 + 40 * $n,
                'idle' => 98412377 + 840 * $n,
                'iowait' => 51234 + 10 * $n,
                'irq' => 0,
                'softirq' => 18765,
                'steal' => 2210,
            ],
            'memory' => ['total_bytes' => 4 * $gib, 'available_bytes' => (int) (2.75 * $gib), 'swap_total_bytes' => 2 * $gib, 'swap_free_bytes' => (int) (1.9 * $gib)],
            'disks' => [
                ['mount' => '/', 'fs_type' => 'ext4', 'total_bytes' => 40 * $gib, 'used_bytes' => 16 * $gib, 'available_bytes' => 22 * $gib, 'inodes_total' => 2621440, 'inodes_free' => 2401234],
                ['mount' => '/boot/efi', 'fs_type' => 'vfat', 'total_bytes' => 104857600, 'used_bytes' => 6291456, 'available_bytes' => 98566144, 'inodes_total' => 0, 'inodes_free' => 0],
            ],
            'network' => ['rx_bytes' => 52311086811 + 250000 * $n, 'tx_bytes' => 9814555121 + 60000 * $n],
            'services' => $services,
        ];
        foreach ($this->metricsChanges as $change) {
            $metrics = $change($metrics);
        }

        return $metrics;
    }

    /** @var array<string, list<string>> each log's lines, by source (and site), once asked for */
    private array $logs = [];

    /**
     * Sets a log's lines: a file source's lines, or a journal source's
     * messages; by default each has 300 numbered lines.
     *
     * @param  list<string>  $lines
     */
    public function setLog(string $source, array $lines, ?string $site = null): static
    {
        $this->logs[$source.'/'.$site] = $lines;

        return $this;
    }

    /** @return list<string> */
    private function sourceLines(array $params): array
    {
        $key = $params['source'].'/'.($params['name'] ?? '');
        $this->logs[$key] ??= array_map(fn (int $i): string => "{$params['source']} line {$i}", range(1, 300));

        return $this->logs[$key];
    }

    private function validateLogs(array $params): void
    {
        $site = str_starts_with($params['source'], 'site.');
        $journal = str_starts_with($params['source'], 'journal.');
        $fields = [];
        if ($site !== isset($params['name'])) {
            $fields['name'] = $site ? 'is required for a site\'s log' : 'must be left out for a server log';
        }
        if (isset($params['archive']) && ($journal || ! preg_match('/^[a-z0-9-]+\.log-[0-9]{8}-[0-9]+\.gz\z/', $params['archive']))) {
            $fields['archive'] = 'is not a log archive name';
        }
        if (isset($params['priority']) && (! $journal || $params['priority'] < 0 || $params['priority'] > 7)) {
            $fields['priority'] = 'is only for journal logs, 0 to 7';
        }
        if (isset($params['limit']) && ($params['limit'] < 0 || $params['limit'] > 1000)) {
            $fields['limit'] = 'must be between 1 and 1000';
        }
        if ($fields !== []) {
            throw new AgentValidationFailed('Invalid parameters', $fields);
        }
    }

    /** @return array<string, mixed> logs.list's data: a file source's current file and one archive */
    private function logsList(array $params): array
    {
        $this->validateLogs($params);
        if (str_starts_with($params['source'], 'journal.')) {
            return ['source' => $params['source'], 'current' => null, 'archives' => []];
        }
        $size = array_sum(array_map(fn (string $l): int => strlen($l) + 1, $this->sourceLines($params)));
        $base = match ($params['source']) {
            'site.nginx-access' => 'access.log',
            'site.nginx-error' => 'error.log',
            'site.php-error' => 'php-error.log',
            'file.zimam-audit' => 'audit.log',
            default => basename(str_replace('.', '/', $params['source'])).'.log',
        };
        $now = self::goTime(CarbonImmutable::now('UTC'));

        return ['source' => $params['source'], 'current' => ['name' => $base, 'size' => $size, 'modified' => $now], 'archives' => [
            ['name' => "{$base}-20261101-1761955200.gz", 'size' => 2048, 'modified' => $now],
        ]];
    }

    /** @return array<string, mixed> logs.read's data, from the fake's own lines, as the agent pages them */
    private function logsRead(array $params): array
    {
        $this->validateLogs($params);
        $journal = str_starts_with($params['source'], 'journal.');
        $all = [];
        $offset = 0;
        foreach ($this->sourceLines($params) as $i => $text) {
            $all[] = ['offset' => $offset, 'text' => $text, 'cursor' => 's=fake;i='.($i + 1), 'priority' => $i % 8];
            $offset += strlen($text) + 1;
        }
        $end = $offset;
        if ($journal && isset($params['priority'])) {
            $all = array_values(array_filter($all, fn (array $l): bool => $l['priority'] <= $params['priority']));
        }
        $limit = $params['limit'] ?? 200;
        $index = fn (array $l): int => (int) substr($l['cursor'], strlen('s=fake;i='));
        if (isset($params['search'])) {
            $from = $journal ? (isset($params['cursor']) ? (int) substr($params['cursor'], strlen('s=fake;i=')) : 0) : ($params['offset'] ?? 0);
            $picked = array_values(array_filter($all, fn (array $l): bool => ($journal ? $index($l) > $from : $l['offset'] >= $from) && mb_stripos($l['text'], $params['search']) !== false));
            $stopped = count($picked) >= 2000 ? 'matches' : 'end';
            $picked = array_slice($picked, 0, 2000);
        } elseif ($params['direction'] === 'forward') {
            $from = $journal ? (int) substr($params['cursor'], strlen('s=fake;i=')) : $params['offset'];
            $picked = array_slice(array_values(array_filter($all, fn (array $l): bool => $journal ? $index($l) > $from : $l['offset'] >= $from)), 0, $limit);
            $stopped = '';
        } else {
            $before = $journal ? (isset($params['cursor']) ? (int) substr($params['cursor'], strlen('s=fake;i=')) : PHP_INT_MAX) : ($params['offset'] ?? $end);
            $picked = array_slice(array_values(array_filter($all, fn (array $l): bool => $journal ? $index($l) < $before : $l['offset'] < $before)), -$limit);
            $stopped = '';
        }
        $first = $picked[0] ?? null;
        $last = $picked === [] ? null : $picked[count($picked) - 1];
        if ($journal) {
            $time = self::goTime(CarbonImmutable::now('UTC'));

            return [
                'lines' => [],
                'entries' => array_map(fn (array $l): array => ['cursor' => $l['cursor'], 'time' => $time, 'priority' => $l['priority'], 'unit' => 'fake.service', 'message' => $l['text']], $picked),
                'back_offset' => null,
                'forward_offset' => null,
                'back_cursor' => $first['cursor'] ?? null,
                'forward_cursor' => $last['cursor'] ?? ($params['cursor'] ?? null),
                'search_stopped' => $stopped,
            ];
        }

        return [
            'lines' => array_map(fn (array $l): array => ['offset' => $l['offset'], 'text' => $l['text'], 'cut' => false], $picked),
            'entries' => [],
            'back_offset' => $first !== null && $first['offset'] > 0 ? $first['offset'] : null,
            'forward_offset' => $last === null ? ($params['offset'] ?? $end) : $last['offset'] + strlen($last['text']) + 1,
            'back_cursor' => null,
            'forward_cursor' => null,
            'search_stopped' => $stopped,
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
