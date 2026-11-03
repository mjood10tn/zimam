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

    /** §5.4's site name, anchored with \z (global rule 14). */
    public const NAME_PATTERN = '/^[a-z][a-z0-9-]{0,22}[a-z0-9]\z/';

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
        return new SiteProvision($this->name, $this->primaryDomain->hostname, $this->type, $this->type === self::PHP ? $this->php_version : null);
    }

    /** The address the site answers on until it has a certificate. */
    public function url(): string
    {
        return 'http://'.$this->primaryDomain?->hostname;
    }
}
