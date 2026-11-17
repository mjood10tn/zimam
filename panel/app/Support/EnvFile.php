<?php

namespace App\Support;

use InvalidArgumentException;

/**
 * A .env file as a list of entries, so a key can be set without touching
 * the rest: comments, blank lines, other keys, their order and the line
 * endings stay as they were. An entry is a line, or the lines of a
 * double-quoted value that runs over several, which are kept whole and are
 * never searched for keys. A key that appears more than once is set at its
 * first entry and removed from the later ones, as the last one would
 * otherwise win when Laravel reads the file.
 */
final class EnvFile
{
    /** @param list<list<string>> $entries */
    private function __construct(
        private array $entries,
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

        $entries = [];
        $open = null;
        foreach ($lines as $line) {
            if ($open !== null) {
                $open[] = $line;
                if (self::quoteCount($line) >= 1) {
                    $entries[] = $open;
                    $open = null;
                }

                continue;
            }
            if (self::opensQuotedValue($line)) {
                $open = [$line];

                continue;
            }
            $entries[] = [$line];
        }
        if ($open !== null) {
            $entries[] = $open;
        }

        return new self($entries, $eol, $finalNewline);
    }

    /**
     * @throws InvalidArgumentException for a value with a line break or NUL, which no line of a
     *                                  .env can hold safely
     */
    public function set(string $key, string $value): self
    {
        if (preg_match('/[\r\n\0]/', $value) === 1) {
            throw new InvalidArgumentException("The value of {$key} must not contain a line break or NUL.");
        }
        $pattern = '/^(\s*(?:export\s+)?)'.preg_quote($key, '/').'\s*=/';
        $written = false;
        $entries = [];
        foreach ($this->entries as $entry) {
            if (preg_match($pattern, $entry[0], $m) !== 1) {
                $entries[] = $entry;

                continue;
            }
            if (! $written) {
                $entries[] = [$m[1].$key.'='.self::quote($value)];
                $written = true;
            }
        }
        if (! $written) {
            $entries[] = [$key.'='.self::quote($value)];
            $this->finalNewline = true;
        }
        $this->entries = $entries;

        return $this;
    }

    public function toString(): string
    {
        $lines = array_merge(...$this->entries ?: [[]]);

        return implode($this->eol, $lines).($lines !== [] && $this->finalNewline ? $this->eol : '');
    }

    /** The unescaped double quotes of a line, as phpdotenv counts them when it looks for a value's end. */
    private static function quoteCount(string $line): int
    {
        return preg_match_all('/(?<!\\\\)"/', str_replace('\\\\', '', $line));
    }

    /** A line like KEY="text that goes on: its quote isn't closed on the same line. */
    private static function opensQuotedValue(string $line): bool
    {
        $at = strpos($line, '="');
        if ($at === false) {
            return false;
        }
        $hash = strpos($line, '#');
        if ($hash !== false && $hash < $at) {
            return false;
        }

        return self::quoteCount($line) === 1;
    }

    /**
     * A value that a parser could misread is double-quoted, with backslash,
     * quote and dollar sign escaped: phpdotenv takes \$ as a literal dollar
     * sign, so ${VAR} in a value is not replaced by another variable.
     */
    private static function quote(string $value): string
    {
        if ($value === '' || ! preg_match('/[\s#"\'$=\\\\]/', $value)) {
            return $value;
        }

        return '"'.str_replace(['\\', '"', '$'], ['\\\\', '\\"', '\\$'], $value).'"';
    }
}
