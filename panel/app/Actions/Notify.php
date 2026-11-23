<?php

namespace App\Actions;

use App\Models\AgentJob;
use App\Models\NotificationChannel;
use App\Models\NotificationState;
use App\Models\Site;
use App\Notifications\PanelEvent;

/**
 * Sends an event to every enabled channel subscribed to its type, with
 * R05-D12's debounce kept in notification_states under $key: a repeated
 * event notifies once while it lasts, again every 24 hours, and once when
 * it clears ($clears). An event that never clears (a failed job) so
 * notifies once.
 */
final class Notify
{
    public const REPEAT_HOURS = 24;

    public function event(string $key, PanelEvent $event, bool $clears = false): void
    {
        $state = NotificationState::firstWhere('event_key', $key);
        $active = $state !== null && $state->cleared_at === null;
        if ($clears) {
            if (! $active) {
                return;
            }
            $state->update(['cleared_at' => now()]);
        } elseif ($active) {
            if ($state->last_sent_at->gt(now()->subHours(self::REPEAT_HOURS))) {
                return;
            }
            $state->update(['last_sent_at' => now()]);
        } else {
            NotificationState::updateOrCreate(['event_key' => $key], ['started_at' => now(), 'last_sent_at' => now(), 'cleared_at' => null]);
        }
        $this->send($event);
    }

    /** Sends at once to the enabled channels that take its type, with no debounce: a channel's test, too. */
    public function send(PanelEvent $event): void
    {
        foreach (NotificationChannel::where('enabled', true)->get() as $channel) {
            if (in_array($event->type, $channel->events, true)) {
                $channel->notify(clone $event);
            }
        }
    }

    /** The keys of the warnings that are active now. @return list<string> */
    public function activeKeys(string $prefix): array
    {
        return NotificationState::where('event_key', 'like', $prefix.'%')->whereNull('cleared_at')->pluck('event_key')->all();
    }

    /**
     * What a finished job tells the owner: a deploy's outcome, a failed
     * certificate, failing security checks, any other failure. Backup runs
     * are told by SyncBackupRuns, from their records; cancelled jobs by
     * nobody.
     */
    public function job(AgentJob $job): void
    {
        $site = $job->related instanceof Site ? $job->related->name : null;
        $ctx = ['command' => $job->command, 'site' => $site ?? '', 'error' => (string) $job->error_message];
        if ($job->command === 'site.deploy' && in_array($job->status, [AgentJob::SUCCEEDED, AgentJob::FAILED], true)) {
            $this->event("deploy:{$job->id}", new PanelEvent('deploy', __("zimam.notify.events.deploy_{$job->status}.title", $ctx), __("zimam.notify.events.deploy_{$job->status}.body", $ctx)));

            return;
        }
        if ($job->command === 'security.check' && $job->status === AgentJob::SUCCEEDED) {
            $failing = array_filter($job->result['findings'] ?? [], fn (array $f): bool => ($f['status'] ?? '') === 'fail');
            foreach (['certificate' => fn (array $f): bool => $f['check'] === 'certificate_valid', 'security' => fn (array $f): bool => $f['check'] !== 'certificate_valid'] as $type => $match) {
                $found = array_filter($failing, $match);
                if ($found !== []) {
                    $lines = implode("\n", array_map(fn (array $f): string => "{$f['check']}: {$f['detail']}", $found));
                    $this->event("{$type}:{$job->id}", new PanelEvent($type, __("zimam.notify.events.{$type}.title", ['site' => $site ?? __('zimam.notify.server')]), $lines));
                }
            }

            return;
        }
        if ($job->status !== AgentJob::FAILED || $job->command === 'backup.run') {
            return;
        }
        $type = $job->command === 'cert.issue' ? 'certificate' : 'job_failed';
        $this->event("{$type}:{$job->id}", new PanelEvent($type, __("zimam.notify.events.{$type}_job.title", $ctx), __("zimam.notify.events.{$type}_job.body", $ctx)));
    }
}
