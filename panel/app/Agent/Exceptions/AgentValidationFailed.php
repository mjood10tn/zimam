<?php

namespace App\Agent\Exceptions;

/** The params failed validation; fields() has a message per field (§5.2). */
final class AgentValidationFailed extends AgentException
{
    protected const AGENT_CODE = 'validation_failed';

    /** @param array<string, string> $fields */
    public function __construct(string $message, private readonly array $fields = [])
    {
        parent::__construct($message);
    }

    /** @return array<string, string> */
    public function fields(): array
    {
        return $this->fields;
    }
}
