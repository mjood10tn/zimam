<?php

namespace App\Actions;

use App\Agent\AgentClient;
use App\Agent\Commands\SiteEnvRead;
use App\Agent\Commands\SiteEnvWrite;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentConflict;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentNotFound;
use App\Agent\Exceptions\AgentUnavailable;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\SiteCredential;
use App\Support\EnvFile;
use App\Support\MalformedEnvFile;
use SensitiveParameter;

/**
 * Sets keys in a site's shared .env: reads it, changes only those keys, and
 * writes it back, so every other line stays. It runs right after a
 * password was set, because that is the only moment the panel has the
 * password; it keeps nothing, and a write that finds the site busy is
 * refused rather than queued, so the password never waits in a queue table.
 */
final class WriteCredentialsToEnv
{
    public function __construct(
        private readonly AgentClient $agent,
        private readonly SubmitAgentCommand $submit,
    ) {}

    /**
     * The keys for a site's MySQL connection.
     *
     * @return array<string, string>
     */
    public static function mysqlValues(string $siteName, string $database, #[SensitiveParameter] string $password): array
    {
        return [
            'DB_CONNECTION' => 'mysql',
            'DB_HOST' => '127.0.0.1',
            'DB_PORT' => '3306',
            'DB_DATABASE' => $database,
            'DB_USERNAME' => SiteCredential::usernameFor($siteName),
            'DB_PASSWORD' => $password,
        ];
    }

    /**
     * @param  array<string, string>  $values
     *
     * @throws AgentException when the site can't take a job now, the .env can't be read, the
     *                        result is too big for the agent, or the write is refused
     */
    public function run(Site $site, #[SensitiveParameter] array $values): AgentJob
    {
        // The Environment tab's rule: not while the site is being set up or deleted, or has a job
        // that hasn't finished, since a job waiting for the agent would be overtaken.
        if (($blocker = $site->jobBlocker()) !== null) {
            throw new AgentConflict($blocker);
        }
        try {
            $content = (string) $this->agent->call(new SiteEnvRead($site->name))->data['content'];
        } catch (AgentNotFound) {
            // A site without a .env gets one holding these keys.
            $content = '';
        } catch (AgentBusy|AgentUnavailable $e) {
            // The callers already say the site is busy or the agent doesn't answer.
            throw $e;
        } catch (AgentException $e) {
            throw new AgentConflict(__('zimam.databases.write_env.unreadable', ['reason' => $e->getMessage()]));
        }
        try {
            $env = EnvFile::parse($content);
            foreach ($values as $key => $value) {
                $env->set($key, $value);
            }
            $merged = $env->toString();
        } catch (MalformedEnvFile $e) {
            // Nothing is written: setting a key would lose the lines the open quote swallows.
            throw new AgentConflict(__('zimam.databases.write_env.malformed', ['key' => $e->key, 'line' => $e->envLine]));
        }
        if (SiteEnvWrite::contentError($merged) !== null) {
            throw new AgentConflict(__('zimam.databases.write_env.too_big'));
        }

        return $this->submit->run(new SiteEnvWrite($site->name, $merged), $site, waitWhenBusy: false);
    }
}
