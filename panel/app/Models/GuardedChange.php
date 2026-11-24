<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * A firewall or SSH change the agent reverts by itself unless the owner
 * keeps it before its deadline (R06-D1, D2). Only one is pending at a time.
 */
class GuardedChange extends Model
{
    public const PENDING = 'pending';

    public const CONFIRMED = 'confirmed';

    public const REVERTED = 'reverted';

    public const FIREWALL = 'firewall';

    public const SSH = 'ssh';

    /** Cloudflare's origin restriction (R06-D9), a firewall change on the server. */
    public const CLOUDFLARE = 'cloudflare';

    /** The job commands that may start one, and the kind each makes (cloudflare.mode only when the restriction changes). */
    public const COMMANDS = ['firewall.rules' => self::FIREWALL, 'ssh.settings' => self::SSH, 'cloudflare.mode' => self::CLOUDFLARE];

    protected $fillable = ['agent_change_id', 'kind', 'deadline', 'status', 'params', 'created_by'];

    protected function casts(): array
    {
        return ['deadline' => 'immutable_datetime', 'params' => 'array'];
    }

    public function creator(): BelongsTo
    {
        return $this->belongsTo(User::class, 'created_by');
    }

    /** The change waiting for Keep, if its deadline hasn't passed. */
    public static function current(): ?self
    {
        return self::query()->where('status', self::PENDING)->where('deadline', '>', now())->latest('id')->first();
    }

    /**
     * Marks every pending change past its deadline reverted: its timer has
     * undone it on the server (R06-D1). Returns them, so the page can say so.
     *
     * @return list<self>
     */
    public static function settleExpired(): array
    {
        $expired = self::query()->where('status', self::PENDING)->where('deadline', '<=', now())->get()->all();
        foreach ($expired as $change) {
            $change->update(['status' => self::REVERTED]);
        }

        return $expired;
    }

    /** Why a new firewall or SSH change can't start now (the agent allows one pending), or null. */
    public static function blocker(): ?string
    {
        return match (true) {
            self::current() !== null => __('zimam.server_security.busy'),
            self::starting() => __('zimam.server_security.applying'),
            default => null,
        };
    }

    /** Whether a firewall or SSH job is still under way: its change isn't here yet. */
    public static function starting(): bool
    {
        return AgentJob::whereIn('command', array_keys(self::COMMANDS))->whereNotIn('status', AgentJob::FINISHED)->exists();
    }
}
