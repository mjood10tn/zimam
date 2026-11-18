<?php

namespace Tests\Unit;

use App\Support\CronSchedule;
use DateTimeImmutable;
use DateTimeZone;
use InvalidArgumentException;
use PHPUnit\Framework\Attributes\DataProvider;
use Tests\TestCase;

/**
 * CronSchedule mirrors the agent's ParseCronSchedule (R04-D5), and shows the
 * next runs in the server's zone (ruling R7), with Debian cron's rule for a
 * restricted day of month and day of week.
 */
final class CronScheduleTest extends TestCase
{
    /**
     * The agent's accept/refuse/normalise table, case for case, from
     * agent/contract/cronschedule_test.go (TestCronScheduleAccepts,
     * TestCronScheduleRefuses, TestCronScheduleNormalises): the normal form
     * of what it accepts, null for what it refuses. Keep the two in step.
     *
     * @return array<string, array{string, string|null}>
     */
    public static function grammar(): array
    {
        $accepts = [
            '*/5 * * * *' => '*/5 * * * *',
            '0 3 * * mon-fri' => '0 3 * * 1-5',
            '@daily' => '0 0 * * *',
            '@hourly' => '0 * * * *',
            '@weekly' => '0 0 * * 0',
            '@monthly' => '0 0 1 * *',
            '@yearly' => '0 0 1 1 *',
            '0,30 */2 1-15/3 jan,jul sat-sun' => '0,30 */2 1-15/3 1,7 6-7',
            '59 23 31 12 7' => '59 23 31 12 0',
            '0 0 * * 0' => '0 0 * * 0',
            // TestCronScheduleNormalises
            '0 3 * * MON' => '0 3 * * 1',
            '0 0 1 JAN,Jul *' => '0 0 1 1,7 *',
            '0 0 * * sat-sun' => '0 0 * * 6-7',
            '0 0 * * sun-sun' => '0 0 * * 0-0',
            '0 0 * * 0-sun' => '0 0 * * 0-0',
            '0 0 * * mon-sun' => '0 0 * * 1-7',
            '0 0 * * 7' => '0 0 * * 0',
            '10-50/10 1,2,3 * * *' => '10-50/10 1,2,3 * * *',
        ];
        $refuses = [
            '@reboot', '@annually', '@daily ', '* * * * * *', '* * * *', '', '60 * * * *', '* 24 * * *',
            '* * 0 * *', '* * 32 * *', '* * * 13 *', '* * * * 8', '*/0 * * * *', '1- * * * *', '-1 * * * *',
            '1-2-3 * * * *', '5-1 * * * *', '*/5/2 * * * *', '5/2 * * * *', ', * * * *', '1,,2 * * * *',
            "* * * * *\n", "*\n* * * *", '* * * * * # x', '*/5 * * * * %', '0 3 * * 1%2', '% * * * *',
            '*  * * * *', "*\t* * * *", ' * * * * *', 'foo * * * *', '* * * mon *', '* * * * jan', '0 3 * * monday',
            '*/60 * * * *', '$(x) * * * *', '* * * * 1-', "1 2 3 4 5\x00",
            // TestCronScheduleNeverRendersPercent
            '0 3 * * 1%touch /x',
            // U+017F folds to s in Go's EqualFold; the agent refuses these too.
            "0 3 * * \u{17F}un", "0 3 * * \u{17F}at", "0 0 1 \u{17F}ep *",
        ];
        $table = [];
        foreach ($accepts as $in => $normal) {
            $table["accepts {$in}"] = [$in, $normal];
        }
        foreach ($refuses as $in) {
            $table['refuses '.json_encode($in)] = [$in, null];
        }

        return $table;
    }

    /** The same accept/refuse table as the agent's tests, and the same normal form. */
    #[DataProvider('grammar')]
    public function testScheduleGrammarMatchesAgent(string $input, ?string $normal): void
    {
        if ($normal === null) {
            $this->expectException(InvalidArgumentException::class);
            CronSchedule::parse($input);

            return;
        }
        $schedule = CronSchedule::parse($input);
        $this->assertSame($normal, $schedule->normal());
        // Normalising twice changes nothing, as the agent's test checks.
        $this->assertSame($normal, CronSchedule::parse($normal)->normal());
    }

    public function testErrorsSaySoInWords(): void
    {
        $this->assertNull(CronSchedule::error('0 3 * * mon'));
        $this->assertStringContainsString(__('zimam.schedule.errors.shape'), (string) CronSchedule::error('* * * *'));
        $this->assertStringContainsString('60', (string) CronSchedule::error('60 * * * *'));
        $this->assertNotNull(CronSchedule::error(str_repeat('*', 201)));
    }

    /** @return list<string> */
    private function runs(string $schedule, int $n, string $from, string $zone = 'UTC'): array
    {
        $at = new DateTimeImmutable($from, new DateTimeZone($zone));

        return array_map(fn (DateTimeImmutable $t): string => $t->format('Y-m-d H:i P'), CronSchedule::parse($schedule)->nextRuns($n, $at));
    }

    public function testNextRunsAreAfterTheStartMinute(): void
    {
        $this->assertSame(['2027-01-02 10:05 +00:00', '2027-01-02 10:10 +00:00'], $this->runs('*/5 * * * *', 2, '2027-01-02 10:00:00'));
        $this->assertSame(['2027-01-02 10:05 +00:00'], $this->runs('*/5 * * * *', 1, '2027-01-02 10:04:59'));
        $this->assertSame([], $this->runs('*/5 * * * *', 0, '2027-01-02 10:00:00'));
    }

    /** Cron runs in the server's zone: 03:00 stays 03:00 local across the change to summer time (2027-03-28). */
    public function testNextRunsUseServerTimeZone(): void
    {
        $this->assertSame(
            ['2027-03-27 03:00 +01:00', '2027-03-28 03:00 +02:00', '2027-03-29 03:00 +02:00'],
            $this->runs('0 3 * * *', 3, '2027-03-26 12:00:00', 'Europe/Berlin'),
        );
        // Computed in the zone it is given, not in UTC.
        $this->assertSame(['2027-03-26 03:00 +00:00'], $this->runs('0 3 * * *', 1, '2027-03-25 12:00:00'));
        // Back to winter time (2027-10-31): still 03:00 local.
        $this->assertSame(['2027-10-30 03:00 +02:00', '2027-10-31 03:00 +01:00'], $this->runs('0 3 * * *', 2, '2027-10-29 12:00:00', 'Europe/Berlin'));
    }

    /** When both day fields are restricted, either matches; a field starting with * makes it both (Debian cron). */
    public function testDayOfMonthOrWeekday(): void
    {
        $this->assertSame(
            ['2027-01-08 00:00 +00:00', '2027-01-13 00:00 +00:00', '2027-01-15 00:00 +00:00', '2027-01-22 00:00 +00:00', '2027-01-29 00:00 +00:00'],
            $this->runs('0 0 13 * fri', 5, '2027-01-02 00:00:00'),
        );
        $this->assertSame(
            ['2027-01-13 00:00 +00:00', '2027-02-13 00:00 +00:00'],
            $this->runs('0 0 13 * *', 2, '2027-01-02 00:00:00'),
        );
        $this->assertSame(
            ['2027-01-08 00:00 +00:00', '2027-01-15 00:00 +00:00'],
            $this->runs('0 0 * * fri', 2, '2027-01-02 00:00:00'),
        );
        // */2 starts with *, so day of month AND weekday: odd Fridays.
        $this->assertSame(
            ['2027-01-15 00:00 +00:00', '2027-01-29 00:00 +00:00', '2027-02-05 00:00 +00:00', '2027-02-19 00:00 +00:00', '2027-03-05 00:00 +00:00'],
            $this->runs('0 0 */2 * fri', 5, '2027-01-02 00:00:00'),
        );
    }

    public function testSundayAs0And7(): void
    {
        $weekend = ['2027-01-09 12:00 +00:00', '2027-01-10 12:00 +00:00', '2027-01-16 12:00 +00:00'];
        $this->assertSame($weekend, $this->runs('0 12 * * sat-sun', 3, '2027-01-04 00:00:00'));
        $this->assertSame($weekend, $this->runs('0 12 * * 6,7', 3, '2027-01-04 00:00:00'));
        $this->assertSame(['2027-01-10 12:00 +00:00', '2027-01-17 12:00 +00:00'], $this->runs('0 12 * * 0', 2, '2027-01-04 00:00:00'));
        $this->assertSame(['2027-01-10 12:00 +00:00'], $this->runs('0 12 * * sun-sun', 1, '2027-01-04 00:00:00'));
        $this->assertSame(['2027-01-05 12:00 +00:00', '2027-01-06 12:00 +00:00'], $this->runs('0 12 * * mon-sun', 2, '2027-01-04 12:00:00'));
    }

    public function testRareAndImpossibleSchedules(): void
    {
        $this->assertSame(
            ['2028-02-29 00:00 +00:00', '2032-02-29 00:00 +00:00', '2036-02-29 00:00 +00:00', '2040-02-29 00:00 +00:00', '2044-02-29 00:00 +00:00'],
            $this->runs('0 0 29 2 *', 5, '2027-01-01 00:00:00'),
        );
        $this->assertSame([], $this->runs('0 0 31 2 *', 5, '2027-01-01 00:00:00'));
        $this->assertSame(['2028-01-01 00:00 +00:00', '2029-01-01 00:00 +00:00'], $this->runs('@yearly', 2, '2027-01-01 00:00:00'));
    }
}
