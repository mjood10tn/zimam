<?php

namespace App\Actions;

use App\Agent\AgentClient;
use App\Agent\Commands\SiteEnvRead;
use App\Agent\Commands\SiteEnvWrite;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentNotFound;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\SiteCredential;
use App\Support\EnvFile;
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
     * @throws AgentException when the .env can't be read or the write is refused
     */
    public function run(Site $site, #[SensitiveParameter] array $values): AgentJob
    {
        try {
            $content = (string) $this->agent->call(new SiteEnvRead($site->name))->data['content'];
        } catch (AgentNotFound) {
            // A site without a .env gets one holding these keys.
            $content = '';
        }
        $env = EnvFile::parse($content);
        foreach ($values as $key => $value) {
            $env->set($key, $value);
        }

        return $this->submit->run(new SiteEnvWrite($site->name, $env->toString()), $site, waitWhenBusy: false);
    }
}
