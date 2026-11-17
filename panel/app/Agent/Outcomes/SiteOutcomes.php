<?php

namespace App\Agent\Outcomes;

use App\Actions\SyncMysql;
use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\SiteCredential;
use Illuminate\Support\Facades\DB;

/** What site.provision's, site.php's, site.security's and site.destroy's outcomes change in sites (§7.4, §9.2, §9.5, §9.6, §9.8). */
final class SiteOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        $outcomes->register('site.provision', function (AgentJob $job, AgentJobStatus $status): void {
            $site = $job->related;
            if (! $site instanceof Site) {
                return;
            }
            if ($status->status === AgentJob::SUCCEEDED) {
                $site->update([
                    'status' => Site::ACTIVE,
                    'template_version' => is_array($status->result) ? ($status->result['template_version'] ?? null) : null,
                    'last_error' => null,
                ]);

                return;
            }
            $site->update([
                'status' => Site::FAILED,
                'last_error' => $status->status === AgentJob::CANCELLED ? __('zimam.sites.cancelled') : $status->message,
            ]);
        });
        // A switch that didn't finish leaves the site serving on its old
        // version (§9.5), so it stays active; only the reason is kept.
        $outcomes->register('site.php', function (AgentJob $job, AgentJobStatus $status): void {
            $site = $job->related;
            if (! $site instanceof Site) {
                return;
            }
            if ($status->status === AgentJob::SUCCEEDED && is_array($status->result) && isset($status->result['php_version'])) {
                $site->update(['php_version' => $status->result['php_version'], 'last_error' => null]);

                return;
            }
            $site->update([
                'last_error' => $status->status === AgentJob::CANCELLED ? __('zimam.sites.switch.cancelled') : $status->message,
            ]);
        });
        // The settings are stored once the agent has applied them (§9.6); a
        // failed apply rolled every file back, so the stored ones still hold.
        $outcomes->register('site.security', function (AgentJob $job, AgentJobStatus $status): void {
            $site = $job->related;
            if (! $site instanceof Site) {
                return;
            }
            if ($status->status !== AgentJob::SUCCEEDED) {
                $site->update(['last_error' => $status->status === AgentJob::CANCELLED ? __('zimam.security.cancelled') : $status->message]);

                return;
            }
            $p = $job->params;
            $site->securitySettings()->firstOrCreate()->fill([
                'force_https' => (bool) $p['force_https'],
                'hsts_max_age' => $p['hsts']['max_age'],
                'hsts_include_subdomains' => (bool) $p['hsts']['include_subdomains'],
                'headers_preset' => (bool) $p['headers_preset'],
                'block_risky_functions' => (bool) $p['block_risky_functions'],
                'restrict_to_site_folder' => (bool) $p['restrict_to_site_folder'],
                'ip_allow' => $p['ip_allow'],
                'ip_deny' => $p['ip_deny'],
                'password_protection' => (bool) $p['password_protection']['enabled'],
            ])->save();
            $site->update(['last_error' => null]);
        });
        // The site's rows go once the server has nothing of it; domains and
        // security settings follow through their cascades. A destroy that
        // didn't finish leaves the site failed, so Delete runs it again.
        $outcomes->register('site.destroy', function (AgentJob $job, AgentJobStatus $status): void {
            $site = $job->related;
            if (! $site instanceof Site) {
                return;
            }
            if ($status->status === AgentJob::SUCCEEDED) {
                $site->delete();

                return;
            }
            $site->update([
                'status' => Site::FAILED,
                'last_error' => $status->status === AgentJob::CANCELLED ? __('zimam.sites.delete.cancelled') : $status->message,
            ]);
            self::forgetRemovedUsers($site, $status);
            // Some of its databases may be gone already.
            if ($job->params['drop_databases'] ?? false) {
                DB::afterCommit(fn () => app(SyncMysql::class)->run());
            }
        });
    }

    /**
     * Drops the credential rows of the users a failed destroy removed. The
     * agent's result says which of the two are gone; a user that is still
     * there keeps its row, its date and its Rotate button. A destroy that was
     * cancelled before it started touched nothing. Any other answer (an agent
     * from before the field, or a run stopped without a result) can't say how
     * far the agent got, so the rows go, as they always did: a password
     * change would create the user again, and no row may offer to rotate the
     * password of a user that may be gone.
     */
    private static function forgetRemovedUsers(Site $site, AgentJobStatus $status): void
    {
        if ($status->status === AgentJob::CANCELLED && $status->startedAt === null) {
            return;
        }
        $removed = is_array($status->result) ? ($status->result['removed'] ?? null) : null;
        $query = SiteCredential::where('site_id', $site->id);
        if (is_array($removed)) {
            $gone = array_keys(array_filter([
                SiteCredential::MYSQL => ($removed['mysql_user'] ?? null) === true,
                SiteCredential::VALKEY => ($removed['valkey_user'] ?? null) === true,
            ]));
            $query->whereIn('kind', $gone);
        }
        $query->delete();
    }
}
