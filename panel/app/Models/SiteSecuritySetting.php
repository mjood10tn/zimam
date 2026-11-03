<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** A site's security settings (§7.6, §9.6); the defaults until site.security arrives (Task 33). */
class SiteSecuritySetting extends Model
{
    protected $fillable = [
        'force_https', 'hsts_max_age', 'hsts_include_subdomains', 'headers_preset', 'block_risky_functions',
        'restrict_to_site_folder', 'ip_allow', 'ip_deny', 'password_protection',
    ];

    /** §9.6's defaults: the header preset on, the rest off or empty. */
    protected $attributes = [
        'force_https' => false,
        'hsts_max_age' => null,
        'hsts_include_subdomains' => false,
        'headers_preset' => true,
        'block_risky_functions' => false,
        'restrict_to_site_folder' => false,
        'ip_allow' => '[]',
        'ip_deny' => '[]',
        'password_protection' => false,
    ];

    protected function casts(): array
    {
        return [
            'force_https' => 'boolean',
            'hsts_max_age' => 'integer',
            'hsts_include_subdomains' => 'boolean',
            'headers_preset' => 'boolean',
            'block_risky_functions' => 'boolean',
            'restrict_to_site_folder' => 'boolean',
            'ip_allow' => 'array',
            'ip_deny' => 'array',
            'password_protection' => 'boolean',
        ];
    }

    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }
}
