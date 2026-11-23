<?php

namespace App\Console\Commands;

use App\Actions\Notify;
use App\Agent\AgentClient;
use App\Agent\Commands\SystemMetrics;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentUnavailable;
use App\Models\MetricSample;
use App\Notifications\PanelEvent;
use App\Support\MetricWarnings;
use Carbon\CarbonImmutable;
use Illuminate\Console\Command;

/**
 * Stores one system.metrics answer (§7.5). Scheduled every minute. It is
 * also the panel's minute tick for notifications (R05-D12): each warning
 * notifies when it starts and clears, and an agent that can't be reached
 * notifies the same way.
 */
final class SampleMetrics extends Command
{
    private const AGENT_KEY = 'agent:unreachable';

    private const WARNING_PREFIX = 'warning:';

    protected $signature = 'zimam:sample-metrics';

    protected $description = 'Record one sample of the server’s metrics';

    public function handle(AgentClient $agent, Notify $notify): int
    {
        try {
            $data = $agent->call(new SystemMetrics)->data;
        } catch (AgentException $e) {
            if ($e instanceof AgentUnavailable) {
                $notify->event(self::AGENT_KEY, new PanelEvent('agent', __('zimam.notify.events.agent.title'), $e->getMessage()));
            }
            // No answer, no row: the chart shows a gap rather than zeros.
            $this->error("The agent didn't answer: {$e->getMessage()}");

            return self::FAILURE;
        }
        $notify->event(self::AGENT_KEY, new PanelEvent('agent', __('zimam.notify.events.agent.cleared'), __('zimam.notify.events.agent.cleared_body'), cleared: true), clears: true);
        $now = CarbonImmutable::now();
        $warnings = MetricWarnings::from($data, $now);
        MetricSample::query()->create(['sampled_at' => $now] + MetricSample::attributesFrom($data));

        $active = [];
        foreach ($warnings as $w) {
            $key = self::WARNING_PREFIX.$w['key'];
            $active[] = $key;
            $notify->event($key, new PanelEvent(self::type($key), $w['title'], $w['detail']));
        }
        foreach (array_diff($notify->activeKeys(self::WARNING_PREFIX), $active) as $key) {
            $notify->event($key, new PanelEvent(self::type($key), __('zimam.notify.events.cleared.title', ['what' => substr($key, strlen(self::WARNING_PREFIX))]), __('zimam.notify.events.cleared.body'), cleared: true), clears: true);
        }

        return self::SUCCESS;
    }

    /** A worker's crash loop is its own event; every other warning is monitoring's. */
    private static function type(string $key): string
    {
        return str_starts_with($key, self::WARNING_PREFIX.'worker-crashloop:') ? 'workers' : 'monitoring';
    }
}
