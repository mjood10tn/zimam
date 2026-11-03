<?php

namespace App\Console\Commands;

use App\Agent\AgentClient;
use App\Agent\Commands\SystemMetrics;
use App\Agent\Exceptions\AgentException;
use App\Models\MetricSample;
use Illuminate\Console\Command;

/** Stores one system.metrics answer (§7.5). Scheduled every minute. */
final class SampleMetrics extends Command
{
    protected $signature = 'zimam:sample-metrics';

    protected $description = 'Record one sample of the server’s metrics';

    public function handle(AgentClient $agent): int
    {
        try {
            $data = $agent->call(new SystemMetrics)->data;
        } catch (AgentException $e) {
            // No answer, no row: the chart shows a gap rather than zeros.
            $this->error("The agent didn't answer: {$e->getMessage()}");

            return self::FAILURE;
        }
        MetricSample::query()->create(['sampled_at' => now()] + MetricSample::attributesFrom($data));

        return self::SUCCESS;
    }
}
