<?php

namespace App\Agent\Exceptions;

use RuntimeException;

/**
 * An agent call that didn't succeed. Each §5.2 error code has its own
 * subclass; this class itself means the agent's reply broke the protocol.
 */
class AgentException extends RuntimeException
{
    /** The §5.2 error code; null when the agent sent no error line. */
    protected const AGENT_CODE = null;

    private const CLASSES = [
        AgentInvalidRequest::AGENT_CODE => AgentInvalidRequest::class,
        AgentUnsupportedProtocol::AGENT_CODE => AgentUnsupportedProtocol::class,
        AgentUnknownCommand::AGENT_CODE => AgentUnknownCommand::class,
        AgentValidationFailed::AGENT_CODE => AgentValidationFailed::class,
        AgentForbidden::AGENT_CODE => AgentForbidden::class,
        AgentBusy::AGENT_CODE => AgentBusy::class,
        AgentNotFound::AGENT_CODE => AgentNotFound::class,
        AgentConflict::AGENT_CODE => AgentConflict::class,
        AgentJobFailed::AGENT_CODE => AgentJobFailed::class,
        AgentInternal::AGENT_CODE => AgentInternal::class,
    ];

    public function agentCode(): ?string
    {
        return static::AGENT_CODE;
    }

    /**
     * The exception for an error line, {"type":"error","code",...}.
     *
     * @param  array<string, mixed>  $line
     */
    public static function fromErrorLine(array $line): self
    {
        $code = $line['code'] ?? null;
        $message = is_string($line['message'] ?? null) ? $line['message'] : 'The agent reported an error';
        if ($code === AgentValidationFailed::AGENT_CODE) {
            return new AgentValidationFailed($message, is_array($line['fields'] ?? null) ? $line['fields'] : []);
        }
        if (is_string($code) && isset(self::CLASSES[$code])) {
            return new (self::CLASSES[$code])($message);
        }

        return new self('The agent answered with the unknown error code '.json_encode($code).": {$message}");
    }
}
