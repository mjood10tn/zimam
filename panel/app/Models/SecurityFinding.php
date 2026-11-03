<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** The latest result of one security check (§7.6, §9.7), upserted by each run. */
class SecurityFinding extends Model
{
    public const PASS = 'pass';

    public const WARN = 'warn';

    public const FAIL = 'fail';

    public const INCONCLUSIVE = 'inconclusive';

    /** §9.7's checks, in its order. */
    public const SITE_CHECKS = ['dotfiles_blocked', 'config_matches_template', 'no_world_writable_files', 'php_version_supported', 'pool_runs_as_site_user', 'home_permissions', 'certificate_valid', 'https_redirect'];

    public const SERVER_CHECKS = ['firewall_active', 'ssh_password_login', 'ssh_root_login', 'security_updates_pending', 'reboot_required', 'stock_www_pools_removed', 'agent_socket_permissions'];

    protected $fillable = ['scope', 'site_id', 'check', 'status', 'detail', 'checked_at'];

    protected function casts(): array
    {
        return ['checked_at' => 'immutable_datetime'];
    }

    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }
}
