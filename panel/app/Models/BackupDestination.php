<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;
use Illuminate\Database\Eloquent\Relations\MorphMany;

/**
 * Where backups go (v0.5.0): S3-compatible storage or SFTP, encrypted by an
 * rclone crypt remote on the server (R05-D2). The panel keeps only the
 * non-secret settings; the S3 credentials and the encryption keys are in
 * the server's rclone.conf only (R05-D3). The patterns mirror the agent's
 * contract types, anchored with \z.
 *
 * @property array<string, mixed> $settings
 */
class BackupDestination extends Model
{
    public const S3 = 's3';

    public const SFTP = 'sftp';

    public const TYPES = [self::S3, self::SFTP];

    public const SAVING = 'saving';

    public const ACTIVE = 'active';

    public const FAILED = 'failed';

    public const DELETING = 'deleting';

    /** A destination's or a plan's id: DestinationID and PlanID. */
    public const KEY_PATTERN = '/^[a-z][a-z0-9-]{0,22}[a-z0-9]\z/';

    public const S3_ENDPOINT_PATTERN = '/^https:\/\/([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+([a-z]{1,63}|xn--[a-z0-9-]{0,58}[a-z0-9])(:([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5]))?\z/';

    public const S3_REGION_PATTERN = '/^[a-z0-9-]{1,32}\z/';

    public const S3_BUCKET_PATTERN = '/^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]\z/';

    public const S3_BUCKET_BAD_PATTERN = '/\.\.|\.-|-\.|^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+\z/';

    public const S3_CREDENTIAL_PATTERN = '/^[A-Za-z0-9\/+=_.-]{1,256}\z/';

    /** A bucket prefix or SFTP folder: lowercase segments, no dot at all. */
    public const PATH_PATTERN = '/^([a-z0-9_-]+(\/[a-z0-9_-]+)*)?\z/';

    public const PATH_MAX_LENGTH = 128;

    public const SFTP_HOST_PATTERN = '/^(([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+([a-z]{1,63}|xn--[a-z0-9-]{0,58}[a-z0-9])|[0-9a-f.:]+)\z/';

    public const SFTP_USER_PATTERN = '/^[a-z_][a-z0-9_-]{0,31}\z/';

    public const FINGERPRINT_PATTERN = '/^SHA256:[A-Za-z0-9+\/]{43}\z/';

    /** The crypt password and salt: 32 random bytes, base64url without padding. */
    public const SECRET_PATTERN = '/^[A-Za-z0-9_-]{43}\z/';

    /** How many characters of the password the owner retypes before it is sent (R05-D3). */
    public const CHECK_LENGTH = 6;

    protected $fillable = ['key', 'type', 'settings', 'public_key', 'host_key_fingerprint', 'status', 'last_tested_at', 'last_error'];

    protected function casts(): array
    {
        return ['settings' => 'array', 'last_tested_at' => 'datetime'];
    }

    public function plans(): HasMany
    {
        return $this->hasMany(BackupPlan::class);
    }

    public function jobs(): MorphMany
    {
        return $this->morphMany(AgentJob::class, 'related');
    }

    /** One crypt password or salt, as the agent takes it. */
    public static function generateSecret(): string
    {
        return rtrim(strtr(base64_encode(random_bytes(32)), '+/', '-_'), '=');
    }

    /** Where it points, in words: the bucket and prefix, or the login and host. */
    public function location(): string
    {
        $s = $this->settings;
        $path = (string) ($s['prefix'] ?? $s['path'] ?? '');
        $path = $path === '' ? '' : '/'.$path;

        return $this->type === self::S3
            ? "{$s['bucket']}{$path} · {$s['endpoint']}"
            : "{$s['user']}@{$s['host']}:{$s['port']}{$path}";
    }

    public function hasUnfinishedJob(): bool
    {
        return $this->jobs()->whereNotIn('status', AgentJob::FINISHED)->exists();
    }
}
