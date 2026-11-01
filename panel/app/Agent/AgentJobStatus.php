<?php

namespace App\Agent;

use Carbon\CarbonImmutable;

/** A job's state, as job.status and the end of job.follow report it (§5.7). */
final class AgentJobStatus
{
    public const FINISHED = ['succeeded', 'failed', 'cancelled'];

    /** @param array<string, mixed> $data the result's data, as the agent sent it */
    private function __construct(
        public readonly string $job,
        public readonly string $command,
        public readonly string $subject,
        public readonly string $status,
        public readonly ?string $errorCode,
        public readonly string $message,
        public readonly mixed $result,
        public readonly ?CarbonImmutable $startedAt,
        public readonly ?CarbonImmutable $finishedAt,
        public readonly int $logSize,
        public readonly array $data,
    ) {}

    /** @param array<string, mixed> $data */
    public static function fromArray(array $data): self
    {
        return new self(
            job: (string) ($data['job'] ?? ''),
            command: (string) ($data['command'] ?? ''),
            subject: (string) ($data['subject'] ?? ''),
            status: (string) ($data['status'] ?? ''),
            errorCode: isset($data['error_code']) ? (string) $data['error_code'] : null,
            message: (string) ($data['message'] ?? ''),
            result: $data['result'] ?? null,
            startedAt: self::time($data['started_at'] ?? null),
            finishedAt: self::time($data['finished_at'] ?? null),
            logSize: (int) ($data['log_size'] ?? 0),
            data: $data,
        );
    }

    public function isFinished(): bool
    {
        return in_array($this->status, self::FINISHED, true);
    }

    /** Go writes RFC 3339 with up to nine fractional digits, trailing zeros trimmed. */
    private static function time(mixed $value): ?CarbonImmutable
    {
        return is_string($value) && $value !== '' ? CarbonImmutable::parse($value)->utc() : null;
    }
}
