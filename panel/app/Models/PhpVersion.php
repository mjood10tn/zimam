<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\MorphMany;

/** One PHP version on the server (§7.6, §8), as system.status last reported it. */
class PhpVersion extends Model
{
    public const INSTALLING = 'installing';

    public const INSTALLED = 'installed';

    public const UNINSTALLING = 'uninstalling';

    public const FAILED = 'failed';

    protected $fillable = ['version', 'status', 'is_default', 'is_panel', 'in_use', 'eol_date'];

    protected function casts(): array
    {
        return [
            'is_default' => 'boolean',
            'is_panel' => 'boolean',
            'in_use' => 'boolean',
            'eol_date' => 'immutable_date',
        ];
    }

    /** The php.* jobs whose outcome updates this version. */
    public function jobs(): MorphMany
    {
        return $this->morphMany(AgentJob::class, 'related');
    }

    /** A job for this version is waiting, queued or running, so its outcome will set the status. */
    public function hasUnfinishedJob(): bool
    {
        return $this->jobs()->whereNotIn('status', AgentJob::FINISHED)->exists();
    }

    /** Why this version can't be uninstalled now, or null if it can (§8.4). */
    public function uninstallBlocker(): ?string
    {
        $key = match (true) {
            $this->is_panel => 'panel',
            $this->is_default => 'default',
            $this->in_use => 'in_use',
            $this->status === self::FAILED => 'not_installed',
            $this->status !== self::INSTALLED => 'busy',
            default => null,
        };

        return $key === null ? null : __("zimam.php.uninstall.blocked.{$key}", ['version' => $this->version]);
    }
}
