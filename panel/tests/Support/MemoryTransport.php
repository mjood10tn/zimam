<?php

namespace Tests\Support;

use App\Agent\AgentTransport;

/**
 * A transport whose agent answers every request with the same prepared
 * reply, read from a memory stream. It keeps each request line and read
 * timeout it was given.
 */
final class MemoryTransport implements AgentTransport
{
    /** @var list<string> */
    public array $lines = [];

    /** @var list<int|null> */
    public array $timeouts = [];

    public function __construct(private string $reply) {}

    /** @param array<string, mixed> ...$lines reply lines, encoded as the agent encodes them */
    public static function replying(array ...$lines): self
    {
        return new self(implode('', array_map(
            fn (array $line): string => json_encode($line, JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES)."\n",
            $lines,
        )));
    }

    public function send(string $line, ?int $readTimeout)
    {
        $this->lines[] = $line;
        $this->timeouts[] = $readTimeout;
        $reply = fopen('php://memory', 'w+b');
        fwrite($reply, $this->reply);
        rewind($reply);

        return $reply;
    }
}
