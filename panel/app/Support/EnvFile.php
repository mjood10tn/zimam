<?php

namespace App\Support;

/**
 * A .env file as a list of lines, so a key can be set without touching the
 * rest: comments, blank lines, other keys, their order and the line endings
 * stay as they were. A key that appears more than once is set at its first
 * line and removed from the later ones, as the last one would otherwise win
 * when Laravel reads the file.
 */
final class EnvFile
{
    /** @param list<string> $lines */
    private function __construct(
        private array $lines,
        private readonly string $eol,
        private bool $finalNewline,
    ) {}

    public static function parse(string $text): self
    {
        $eol = str_contains($text, "\r\n") ? "\r\n" : "\n";
        $lines = preg_split('/\r\n|\n/', $text);
        // Text that ends in a line ending splits into a last, empty piece.
        $finalNewline = end($lines) === '';
        if ($finalNewline) {
            array_pop($lines);
        }

        return new self($lines, $eol, $finalNewline);
    }

    public function set(string $key, string $value): self
    {
        $pattern = '/^(\s*(?:export\s+)?)'.preg_quote($key, '/').'\s*=/';
        $written = false;
        $lines = [];
        foreach ($this->lines as $line) {
            if (preg_match($pattern, $line, $m) !== 1) {
                $lines[] = $line;

                continue;
            }
            if (! $written) {
                $lines[] = $m[1].$key.'='.self::quote($value);
                $written = true;
            }
        }
        if (! $written) {
            $lines[] = $key.'='.self::quote($value);
            $this->finalNewline = true;
        }
        $this->lines = $lines;

        return $this;
    }

    public function toString(): string
    {
        return implode($this->eol, $this->lines).($this->lines !== [] && $this->finalNewline ? $this->eol : '');
    }

    /** A value that a parser could misread is double-quoted, with backslash and quote escaped. */
    private static function quote(string $value): string
    {
        if ($value === '' || ! preg_match('/[\s#"\'$=\\\\]/', $value)) {
            return $value;
        }

        return '"'.str_replace(['\\', '"'], ['\\\\', '\\"'], $value).'"';
    }
}
