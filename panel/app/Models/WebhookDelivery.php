<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * A GitHub delivery already handled, kept 24 hours so a redelivery deploys
 * once (R02-D11). Each push leaves two rows: GitHub's delivery id, and a
 * hash of the signed body, since the id is a header the signature doesn't
 * cover and a replay can change it.
 */
class WebhookDelivery extends Model
{
    /** How long a delivery id is remembered. */
    public const KEEP_HOURS = 24;

    /** How long before its deployment row a delivery may have been recorded: one submit to the agent. */
    public const FORGET_WINDOW_SECONDS = 60;

    /** The row for a signed body, whatever delivery id came with it. */
    public static function bodyKey(string $body): string
    {
        return 'sha256:'.hash('sha256', $body);
    }

    /**
     * Forgets the delivery that started $deployment, which never ran, so
     * GitHub's redelivery isn't answered as a duplicate. The table has no
     * link to the deployment; the delivery is the site's from the moment
     * before the row was made. Another delivery of that minute goes too,
     * which at worst lets it be redelivered: the agent refuses a commit
     * older than the live one.
     */
    public static function forgetFor(Deployment $deployment): void
    {
        if ($deployment->created_at === null) {
            return;
        }
        self::query()
            ->where('site_id', $deployment->site_id)
            ->whereBetween('received_at', [$deployment->created_at->copy()->subSeconds(self::FORGET_WINDOW_SECONDS), $deployment->created_at])
            ->delete();
    }

    public $timestamps = false;

    protected $fillable = ['delivery_id', 'site_id', 'received_at'];

    protected function casts(): array
    {
        return ['received_at' => 'immutable_datetime'];
    }

    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }
}
