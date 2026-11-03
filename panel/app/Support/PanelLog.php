<?php

namespace App\Support;

/**
 * The panel's own Laravel log, read by the panel itself from its storage
 * folder (§11.1), in logs.read's shape: lines with byte offsets, back from
 * the end or an offset, forward from one, or a plain-text search.
 */
final class PanelLog
{
    public const SOURCE = 'panel.laravel';

    private const WINDOW = 1 << 20;

    public function __construct(private readonly ?string $path = null) {}

    public function path(): string
    {
        return $this->path ?? storage_path('logs/laravel.log');
    }

    /** @return array{lines: list<array{offset: int, text: string}>, back_offset: ?int, forward_offset: int, search_stopped: string} */
    public function read(string $direction = 'back', ?int $offset = null, int $limit = 200, string $search = ''): array
    {
        $path = $this->path();
        $size = is_file($path) && ! is_link($path) ? (int) filesize($path) : 0;
        if ($size === 0) {
            return ['lines' => [], 'back_offset' => null, 'forward_offset' => 0, 'search_stopped' => $search === '' ? '' : 'end'];
        }
        if ($search !== '') {
            // A search looks through at most 16 MiB at a time, to stay inside PHP's memory.
            $start = min($offset ?? 0, $size);
            $end = min($size, $start + 16 * self::WINDOW);
            $lines = array_values(array_filter(self::lines($path, $start, $end), fn (array $l): bool => mb_stripos($l['text'], $search) !== false));
            $stopped = count($lines) >= 2000 ? 'matches' : ($end < $size ? 'size' : 'end');

            return ['lines' => array_slice($lines, 0, 2000), 'back_offset' => null, 'forward_offset' => $end, 'search_stopped' => $stopped];
        }
        if ($direction === 'forward') {
            $lines = array_slice(self::lines($path, min($offset ?? 0, $size), min($size, ($offset ?? 0) + self::WINDOW)), 0, $limit);
            $end = $lines === [] ? min($offset ?? 0, $size) : end($lines)['offset'] + strlen(end($lines)['text']) + 1;

            return ['lines' => $lines, 'back_offset' => null, 'forward_offset' => $end, 'search_stopped' => ''];
        }
        $before = min($offset ?? $size, $size);
        $start = max(0, $before - self::WINDOW);
        $lines = self::lines($path, $start, $before);
        if ($start > 0) {
            array_shift($lines); // a partial line
        }
        $lines = array_slice($lines, -$limit);

        return ['lines' => $lines, 'back_offset' => ($lines[0]['offset'] ?? 0) > 0 ? $lines[0]['offset'] : null, 'forward_offset' => $before, 'search_stopped' => ''];
    }

    /** @return list<array{offset: int, text: string}> the complete lines between two offsets */
    private static function lines(string $path, int $from, int $to): array
    {
        $h = fopen($path, 'rb');
        fseek($h, $from);
        $data = $to > $from ? (string) fread($h, $to - $from) : '';
        fclose($h);
        $out = [];
        $at = $from;
        while (($nl = strpos($data, "\n")) !== false) {
            $out[] = ['offset' => $at, 'text' => mb_strcut(substr($data, 0, $nl), 0, 16384)];
            $at += $nl + 1;
            $data = substr($data, $nl + 1);
        }

        return $out;
    }
}
