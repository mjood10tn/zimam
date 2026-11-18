<?php

namespace App\Models;

use App\Agent\Commands\SiteProvision;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;
use Illuminate\Database\Eloquent\Relations\HasOne;
use Illuminate\Database\Eloquent\Relations\MorphMany;

/** A site (§7.6, §9). Its files and config live on the server; the agent keeps its own state file (D15). */
class Site extends Model
{
    public const PROVISIONING = 'provisioning';

    public const ACTIVE = 'active';

    public const FAILED = 'failed';

    public const DELETING = 'deleting';

    public const STATIC = 'static';

    public const PHP = 'php';

    /** A PHP site with a shared .env and storage/ (R02-D8). */
    public const LARAVEL = 'laravel';

    /** The types a site can be created as. */
    public const TYPES = [self::PHP, self::LARAVEL, self::STATIC];

    /** §5.4's site name, anchored with \z (global rule 14). */
    /** A site name's shape without anchors, for a route constraint. */
    public const NAME_SHAPE = '[a-z][a-z0-9-]{0,22}[a-z0-9]';

    public const NAME_PATTERN = '/^'.self::NAME_SHAPE.'\z/';

    public const RESERVED_NAMES = ['zimam', 'panel', 'default', 'acme'];

    protected $fillable = ['name', 'type', 'php_version', 'status', 'web_directory', 'template_version', 'last_error'];

    protected function casts(): array
    {
        return ['template_version' => 'integer'];
    }

    public function domains(): HasMany
    {
        return $this->hasMany(Domain::class);
    }

    public function primaryDomain(): HasOne
    {
        return $this->hasOne(Domain::class)->where('is_primary', true);
    }

    /** Whether the site has a PHP version and pool: PHP and Laravel sites. */
    public function runsPhp(): bool
    {
        return in_array($this->type, [self::PHP, self::LARAVEL], true);
    }

    /** Whether the site is a Laravel app, with Laravel's log in its shared storage (R02-D8). */
    public function isLaravel(): bool
    {
        return $this->type === self::LARAVEL;
    }

    /** The GitHub repository the site deploys from (R02-D4). */
    public function repository(): HasOne
    {
        return $this->hasOne(SiteRepository::class);
    }

    public function deployments(): HasMany
    {
        return $this->hasMany(Deployment::class);
    }

    /** The site's MySQL databases, as the agent last listed them. */
    public function databases(): HasMany
    {
        return $this->hasMany(Database::class);
    }

    /** The site's service users: MySQL's now, Valkey's next. */
    public function credentials(): HasMany
    {
        return $this->hasMany(SiteCredential::class);
    }

    public function securitySettings(): HasOne
    {
        return $this->hasOne(SiteSecuritySetting::class);
    }

    /** The jobs whose outcome updates this site. */
    public function jobs(): MorphMany
    {
        return $this->morphMany(AgentJob::class, 'related');
    }

    /** The site.provision that creates this site, or retries it. */
    public function provisionCommand(): SiteProvision
    {
        return new SiteProvision($this->name, $this->primaryDomain->hostname, $this->type, $this->runsPhp() ? $this->php_version : null);
    }

    /** The password-protection users (§9.6), as bcrypt hashes. */
    public function basicAuthUsers(): HasMany
    {
        return $this->hasMany(SiteBasicAuthUser::class);
    }

    /** The latest result of each of the site's security checks (§9.7). */
    public function securityFindings(): HasMany
    {
        return $this->hasMany(SecurityFinding::class);
    }

    /** The site's queue workers, as last saved from the Workers tab. */
    public function workers(): HasMany
    {
        return $this->hasMany(SiteWorker::class);
    }

    public function certificate(): HasOne
    {
        return $this->hasOne(Certificate::class);
    }

    /**
     * Whether a job on this site, or on its certificate, has yet to end:
     * the agent runs one per site at a time.
     */
    public function hasUnfinishedJob(): bool
    {
        return $this->jobs()->whereNotIn('status', AgentJob::FINISHED)->exists()
            || ($this->certificate?->jobs()->whereNotIn('status', AgentJob::FINISHED)->exists() ?? false)
            || AgentJob::whereNotIn('status', AgentJob::FINISHED)
                ->where('related_type', (new Database)->getMorphClass())
                ->whereIn('related_id', $this->databases()->select('id'))
                ->exists();
    }

    /**
     * Why the site can't take a job now, in words that match its state, or
     * null when it can: it is active and none of its jobs is unfinished.
     */
    public function jobBlocker(): ?string
    {
        return match (true) {
            $this->status !== self::ACTIVE => __("zimam.sites.not_ready.{$this->status}"),
            $this->hasUnfinishedJob() => __('zimam.sites.busy'),
            default => null,
        };
    }

    public function canTakeJob(): bool
    {
        return $this->jobBlocker() === null;
    }

    /** The address the site answers on: HTTPS once its certificate is issued. */
    public function url(): string
    {
        $scheme = $this->certificate?->status === Certificate::ISSUED ? 'https' : 'http';

        return $scheme.'://'.$this->primaryDomain?->hostname;
    }
}
