<?php

namespace Tests\Feature\Dashboard;

use App\Filament\Widgets\Warnings;
use PHPUnit\Framework\Attributes\DataProvider;
use Tests\TestCase;

/** The bulletin headline counts the warnings in force (dashboard brief §3), critical ones first. */
final class HeadlineTest extends TestCase
{
    /** @return array<string, array{bool, list<string>, string}> */
    public static function cases(): array
    {
        return [
            'none' => [true, [], 'No warnings.'],
            'one warning' => [true, ['warning'], 'One warning in force.'],
            'two warnings' => [true, ['warning', 'warning'], 'Two warnings in force.'],
            'one critical' => [true, ['critical'], 'One critical problem.'],
            'three critical' => [true, ['critical', 'critical', 'critical'], 'Three critical problems.'],
            'both' => [true, ['warning', 'critical', 'warning'], 'One critical, two warnings.'],
            'both, one warning' => [true, ['critical', 'warning', 'critical'], 'Two critical, one warning.'],
            'past twelve' => [true, array_fill(0, 13, 'warning'), '13 warnings in force.'],
            'unreachable' => [false, ['warning'], 'Agent unreachable.'],
        ];
    }

    /** @param list<string> $severities */
    #[DataProvider('cases')]
    public function testHeadline(bool $reachable, array $severities, string $expected): void
    {
        $widget = new class extends Warnings
        {
            /** @param list<array{severity: string}> $warnings */
            public function headlineFor(bool $reachable, array $warnings): string
            {
                return $this->headline($reachable, $warnings);
            }
        };

        $this->assertSame($expected, $widget->headlineFor($reachable, array_map(fn (string $severity): array => ['severity' => $severity], $severities)));
    }
}
