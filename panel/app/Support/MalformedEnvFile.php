<?php

namespace App\Support;

use RuntimeException;

/**
 * A .env whose double-quoted value is never closed: everything after it is
 * inside the quote as far as a parser can tell, so no key can be set in it
 * without losing lines.
 */
final class MalformedEnvFile extends RuntimeException
{
    public function __construct(public readonly string $key, public readonly int $envLine)
    {
        parent::__construct("The .env has an unterminated double quote at {$key}, line {$envLine}.");
    }
}
