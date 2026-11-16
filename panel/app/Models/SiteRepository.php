<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use SensitiveParameter;

/**
 * The GitHub repository a site deploys from (R02-D4, R02-D11), with its
 * deploy settings. The webhook secret is stored encrypted, since the
 * webhook needs it back to check signatures; the deploy URL's token only as
 * its SHA-256. Both are shown once, when made.
 */
class SiteRepository extends Model
{
    /**
     * The agent's Repository and Branch patterns (agent/contract/deploy.go),
     * anchored with \z (global rule 9). A value must match the first and not
     * the second.
     */
    public const REPOSITORY_PATTERN = '/^[A-Za-z0-9][A-Za-z0-9-]{0,38}\/[A-Za-z0-9._-]{1,100}\z/';

    public const REPOSITORY_FORBIDDEN = '/\.[gG][iI][tT]\z|\/\.\.?\z/';

    public const BRANCH_PATTERN = '/^[A-Za-z0-9._\/-]{1,200}\z/';

    public const BRANCH_FORBIDDEN = '/^[-\/]|\.\.|\/\/|(^|\/)\.|[\/.]\z|\.lock(\/|\z)/';

    /** The agent's cap on a deploy script, in bytes (contract.MaxTextFileBytes). */
    public const MAX_SCRIPT_BYTES = 65536;

    public const MIN_KEEP_RELEASES = 1;

    public const MAX_KEEP_RELEASES = 20;

    /** R02-D8's deploy preset for Laravel sites, with Composer on. Shell text, run by the site user's bash. */
    public const LARAVEL_PRESET = <<<'SH'
        php artisan storage:link
        php artisan migrate --force
        php artisan config:cache
        php artisan route:cache
        php artisan view:cache

        SH;

    protected $fillable = [
        'repository', 'branch', 'deploy_key_public', 'deploy_key_fingerprint',
        'auto_deploy', 'deploy_script', 'run_composer', 'keep_releases',
    ];

    protected $hidden = ['webhook_secret', 'deploy_token_hash'];

    protected $attributes = [
        'provider' => 'github',
        'auto_deploy' => false,
        'deploy_script' => '',
        'run_composer' => true,
        'keep_releases' => 5,
    ];

    protected function casts(): array
    {
        return [
            'webhook_secret' => 'encrypted',
            'auto_deploy' => 'boolean',
            'run_composer' => 'boolean',
            'keep_releases' => 'integer',
        ];
    }

    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }

    /**
     * A site's first repository, with a new webhook secret and deploy token
     * and the defaults for its type: Composer for PHP sites, the preset for
     * Laravel ones. The token is returned in $revealedDeployToken, the only
     * time it exists in plain.
     */
    public static function createFor(Site $site, string $repository, string $branch): self
    {
        $model = new self([
            'repository' => $repository,
            'branch' => $branch,
            'run_composer' => $site->runsPhp(),
            'deploy_script' => $site->type === Site::LARAVEL ? self::LARAVEL_PRESET : '',
        ]);
        $model->site()->associate($site);
        $model->regenerateWebhookSecret(save: false);
        $model->revealedDeployToken = $model->regenerateDeployToken(save: false);
        $model->save();

        return $model;
    }

    /** The plain deploy token createFor() made; never stored. */
    public ?string $revealedDeployToken = null;

    /** A new webhook secret, which replaces the old one at once. */
    public function regenerateWebhookSecret(bool $save = true): string
    {
        $secret = bin2hex(random_bytes(32));
        $this->webhook_secret = $secret;
        if ($save) {
            $this->save();
        }

        return $secret;
    }

    /** A new deploy token: the old URL stops working. Only its hash is kept. */
    public function regenerateDeployToken(bool $save = true): string
    {
        $token = bin2hex(random_bytes(32));
        $this->deploy_token_hash = hash('sha256', $token);
        if ($save) {
            $this->save();
        }

        return $token;
    }

    public function matchesDeployToken(#[SensitiveParameter] string $token): bool
    {
        return is_string($this->deploy_token_hash) && hash_equals($this->deploy_token_hash, hash('sha256', $token));
    }

    /** Where GitHub sends push events (R02-D11). */
    public function webhookUrl(): string
    {
        return url('hooks/github/'.$this->site->name);
    }

    /** The URL a POST to deploys the site (R02-D11). */
    public function deployUrl(#[SensitiveParameter] string $token): string
    {
        return url('hooks/deploy/'.$this->site->name.'/'.$token);
    }

    /** Whether site.repo has made the deploy key, so the site can deploy. */
    public function isConnected(): bool
    {
        return filled($this->deploy_key_fingerprint);
    }

    public static function isValidRepository(string $value): bool
    {
        return preg_match(self::REPOSITORY_PATTERN, $value) === 1 && preg_match(self::REPOSITORY_FORBIDDEN, $value) === 0;
    }

    public static function isValidBranch(string $value): bool
    {
        return preg_match(self::BRANCH_PATTERN, $value) === 1 && preg_match(self::BRANCH_FORBIDDEN, $value) === 0;
    }

    /** Why a deploy script can't go to the agent, as it checks it: UTF-8, no NUL, at most 64 KiB in bytes. */
    public static function scriptError(string $script): ?string
    {
        return match (true) {
            strlen($script) > self::MAX_SCRIPT_BYTES => __('zimam.deployments.validation.script_size', ['max' => self::MAX_SCRIPT_BYTES]),
            ! mb_check_encoding($script, 'UTF-8') => __('zimam.deployments.validation.script_utf8'),
            str_contains($script, "\0") => __('zimam.deployments.validation.script_nul'),
            default => null,
        };
    }
}
