<?php

namespace App\Support;

use DateTimeImmutable;
use DateTimeInterface;
use DateTimeZone;
use InvalidArgumentException;

/**
 * A cron job's schedule (R04-D5), parsed exactly as the agent's
 * ParseCronSchedule does (agent/contract/cronschedule.go): five fields
 * separated by single spaces of `*`, `a`, `a-b`, either with a step after a slash,
 * and comma lists, with month and weekday names in their own field, or one of
 *
 * @hourly, @daily, @weekly, @monthly, @yearly. Never @reboot, never a %.
 * Weekday 7 is Sunday; the name sun ends a range as 7 unless the range
 * starts at Sunday. normal() is what the agent renders into cron.
 *
 * nextRuns() works in the zone of the time it is given: cron runs in the
 * server's zone (ruling R7), so the caller passes "now" in that zone.
 */
final class CronSchedule
{
    private const MAX_LENGTH = 200;

    private const MACROS = [
        '@hourly' => '0 * * * *', '@daily' => '0 0 * * *', '@weekly' => '0 0 * * 0',
        '@monthly' => '0 0 1 * *', '@yearly' => '0 0 1 1 *',
    ];

    private const MONTHS = ['jan', 'feb', 'mar', 'apr', 'may', 'jun', 'jul', 'aug', 'sep', 'oct', 'nov', 'dec'];

    private const DAYS = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'];

    /** Minute, hour, day of month, month, weekday: [lowest, highest, names, the value of the first name]. */
    private const FIELDS = [
        [0, 59, [], 0],
        [0, 23, [], 0],
        [1, 31, [], 0],
        [1, 12, self::MONTHS, 1],
        [0, 7, self::DAYS, 0],
    ];

    /** The days nextRuns looks ahead: enough for 5 runs of 29 February. */
    private const LOOKAHEAD_DAYS = 366 * 30;

    private function __construct(private readonly string $normal) {}

    /** @throws InvalidArgumentException with the reason in words */
    public static function parse(string $schedule): self
    {
        if (strlen($schedule) > self::MAX_LENGTH) {
            throw new InvalidArgumentException(__('zimam.schedule.errors.too_long'));
        }
        if (isset(self::MACROS[$schedule])) {
            return new self(self::MACROS[$schedule]);
        }
        $parts = explode(' ', $schedule);
        if (count($parts) !== 5) {
            throw new InvalidArgumentException(__('zimam.schedule.errors.shape'));
        }
        $normal = [];
        foreach ($parts as $i => $part) {
            try {
                $normal[] = implode(',', array_map(fn (string $item): string => self::item(self::FIELDS[$i], $item), explode(',', $part)));
            } catch (InvalidArgumentException $e) {
                throw new InvalidArgumentException(__('zimam.schedule.errors.field', ['n' => $i + 1, 'problem' => $e->getMessage()]));
            }
        }

        return new self(implode(' ', $normal));
    }

    /** Why $schedule isn't one the agent takes, or null when it is. */
    public static function error(string $schedule): ?string
    {
        try {
            self::parse($schedule);
        } catch (InvalidArgumentException $e) {
            return $e->getMessage();
        }

        return null;
    }

    /** The form the agent renders: names become numbers and macros their five fields. */
    public function normal(): string
    {
        return $this->normal;
    }

    /**
     * The next $n times it runs after $from, in $from's zone. A time that
     * doesn't exist where the clocks go forward is left out, and one that
     * happens twice where they go back is shown once.
     * shortcut: Debian cron runs a fixed-time job from the skipped hour right
     * after the change; the preview omits that run. Upgrade if it ever matters.
     *
     * @return list<DateTimeImmutable>
     */
    public function nextRuns(int $n, DateTimeInterface $from): array
    {
        $zone = $from->getTimezone();
        [$minutes, $hours, $monthDays, $months, $weekdays] = array_map(self::expand(...), explode(' ', $this->normal), array_keys(self::FIELDS));
        [$domField, $dowField] = [explode(' ', $this->normal)[2], explode(' ', $this->normal)[4]];
        // Debian cron: a day of month or weekday field starting with * makes both
        // have to match; with neither, either does.
        $both = str_starts_with($domField, '*') || str_starts_with($dowField, '*');
        $after = $from->getTimestamp();
        $runs = [];
        // Days are counted on the plain calendar, so a zone's own gaps can't skip one.
        $day = new DateTimeImmutable($from->format('Y-m-d'), new DateTimeZone('UTC'));
        for ($i = 0; $i < self::LOOKAHEAD_DAYS && count($runs) < $n; $i++, $day = $day->modify('+1 day')) {
            [$m, $d, $w] = [(int) $day->format('n'), (int) $day->format('j'), (int) $day->format('w')];
            $dayMatches = $both ? isset($monthDays[$d], $weekdays[$w]) : isset($monthDays[$d]) || isset($weekdays[$w]);
            if (! isset($months[$m]) || ! $dayMatches) {
                continue;
            }
            foreach (array_keys($hours) as $h) {
                foreach (array_keys($minutes) as $min) {
                    $at = new DateTimeImmutable(sprintf('%s %02d:%02d:00', $day->format('Y-m-d'), $h, $min), $zone);
                    if ((int) $at->format('G') === $h && (int) $at->format('i') === $min && $at->getTimestamp() > $after && count($runs) < $n) {
                        $runs[] = $at;
                    }
                }
            }
        }

        return $runs;
    }

    /**
     * The values of a normal-form field, as the keys of a set. Weekday 7 is
     * Sunday, so it is folded onto 0.
     *
     * @return array<int, true>
     */
    private static function expand(string $field, int $index): array
    {
        [$lo, $hi] = self::FIELDS[$index];
        $set = [];
        foreach (explode(',', $field) as $item) {
            [$span, $step] = array_pad(explode('/', $item), 2, '1');
            [$from, $to] = $span === '*' ? [$lo, $hi] : array_map('intval', array_pad(explode('-', $span), 2, $span));
            for ($v = $from; $v <= $to; $v += (int) $step) {
                $set[$index === 4 && $v === 7 ? 0 : $v] = true;
            }
        }
        ksort($set);

        return $set;
    }

    /**
     * One comma item to its normal form (cronField.parseItem).
     *
     * @param  array{int, int, list<string>, int}  $field
     */
    private static function item(array $field, string $item): string
    {
        [$lo, $hi] = $field;
        $parts = explode('/', $item, 2);
        $span = $parts[0];
        $hasStep = count($parts) === 2;
        $stepText = '';
        if ($hasStep) {
            $step = self::number($parts[1]);
            if ($step === null || $step < 1 || $step > $hi) {
                throw new InvalidArgumentException(__('zimam.schedule.errors.step', ['step' => $parts[1], 'max' => $hi]));
            }
            $stepText = '/'.$step;
        }
        if ($span === '*') {
            return '*'.$stepText;
        }
        $range = explode('-', $span, 2);
        $from = self::value($field, $range[0]);
        if (count($range) === 1) {
            if ($hasStep) {
                throw new InvalidArgumentException(__('zimam.schedule.errors.step_needs_range'));
            }
            // Weekday 7 is Sunday.
            if ($field[2] !== [] && $hi === 7 && $from === 7) {
                $from = 0;
            }

            return (string) $from;
        }
        $to = self::value($field, $range[1]);
        // The name sun ends a range after Sunday as 7 (sat-sun is 6-7), but a range
        // that starts at Sunday stays Sunday-only: sun-sun is 0-0, never 0-7.
        if ($hi === 7 && $to === 0 && $from > 0 && self::number($range[1]) === null) {
            $to = 7;
        }
        if ($from > $to) {
            throw new InvalidArgumentException(__('zimam.schedule.errors.backwards', ['range' => $span]));
        }

        return "{$from}-{$to}{$stepText}";
    }

    /**
     * A number or a name (cronField.value).
     *
     * @param  array{int, int, list<string>, int}  $field
     */
    private static function value(array $field, string $text): int
    {
        $n = self::number($text);
        if ($n === null) {
            foreach ($field[2] as $i => $name) {
                if (strcasecmp($text, $name) === 0) {
                    $n = $field[3] + $i;
                }
            }
        }
        if ($n === null) {
            throw new InvalidArgumentException(__('zimam.schedule.errors.not_a_value', ['value' => $text]));
        }
        if ($n < $field[0] || $n > $field[1]) {
            throw new InvalidArgumentException(__('zimam.schedule.errors.out_of_range', ['value' => $n, 'min' => $field[0], 'max' => $field[1]]));
        }

        return $n;
    }

    /** One or two ASCII digits and nothing else (cronNumber): no sign, no space. */
    private static function number(string $text): ?int
    {
        return preg_match('/^[0-9]{1,2}\z/', $text) === 1 ? (int) $text : null;
    }
}
