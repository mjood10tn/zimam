<?php

namespace Tests\Unit;

use App\Filament\Pages\Node;
use PHPUnit\Framework\Attributes\DataProvider;
use PHPUnit\Framework\TestCase;

/**
 * The Node page and the deploy script hint show the Node version, not the
 * Debian package version the agent reports (dpkg-query, ledger R1).
 */
final class NodeDisplayVersionTest extends TestCase
{
    /** @return array<string, array{string, string}> */
    public static function versions(): array
    {
        return [
            'nodesource revision' => ['26.13.1-1nodesource1', '26.13.1'],
            'epoch and revision' => ['1:24.2.0-1nodesource1', '24.2.0'],
            'plain version' => ['26.13.1', '26.13.1'],
            'major only' => ['26', '26'],
            'nothing numeric' => ['unknown', 'unknown'],
        ];
    }

    #[DataProvider('versions')]
    public function testShowsTheNodeVersion(string $package, string $shown): void
    {
        $this->assertSame($shown, Node::displayVersion($package));
    }
}
