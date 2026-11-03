<?php

namespace App\Support;

/**
 * CPU percentage and network rates from two readings of the agent's
 * cumulative counters (§7.5). A counter that went down means the server
 * rebooted in between, so that interval has no value rather than a wrong one.
 */
final class MetricRates
{
    /** The counters that make up the CPUs' time, in ticks (system.metrics' cpu). */
    public const CPU_FIELDS = ['user', 'nice', 'system', 'idle', 'iowait', 'irq', 'softirq', 'steal'];

    /** Idle time: the rest counts as busy, steal included. */
    private const IDLE_FIELDS = ['idle', 'iowait'];

    /**
     * How busy the CPUs were between two readings, 0–100, or null when no
     * tick passed or a counter went down.
     *
     * @param  array<string, int|float>  $before
     * @param  array<string, int|float>  $after
     */
    public static function cpuPercent(array $before, array $after): ?float
    {
        $total = 0.0;
        $idle = 0.0;
        foreach (self::CPU_FIELDS as $field) {
            $delta = ($after[$field] ?? 0) - ($before[$field] ?? 0);
            if ($delta < 0) {
                return null;
            }
            $total += $delta;
            if (in_array($field, self::IDLE_FIELDS, true)) {
                $idle += $delta;
            }
        }
        if ($total <= 0) {
            return null;
        }

        return 100.0 * ($total - $idle) / $total;
    }

    /** A counter's increase per second, or null when no time passed or it went down. */
    public static function perSecond(int|float $before, int|float $after, float $seconds): ?float
    {
        if ($seconds <= 0 || $after < $before) {
            return null;
        }

        return ($after - $before) / $seconds;
    }

    /**
     * The rates over each interval between consecutive readings, in order,
     * each dated by its later reading: one fewer than the readings.
     *
     * @param  iterable<array{at: int|float, cpu: array<string, int|float>, net_rx: int|float, net_tx: int|float}>  $readings  oldest first; at in Unix seconds
     * @return list<array{at: int|float, cpu: ?float, rx: ?float, tx: ?float}>
     */
    public static function series(iterable $readings): array
    {
        $out = [];
        $previous = null;
        foreach ($readings as $reading) {
            if ($previous !== null) {
                $seconds = (float) ($reading['at'] - $previous['at']);
                $out[] = [
                    'at' => $reading['at'],
                    'cpu' => self::cpuPercent($previous['cpu'], $reading['cpu']),
                    'rx' => self::perSecond($previous['net_rx'], $reading['net_rx'], $seconds),
                    'tx' => self::perSecond($previous['net_tx'], $reading['net_tx'], $seconds),
                ];
            }
            $previous = $reading;
        }

        return $out;
    }
}
