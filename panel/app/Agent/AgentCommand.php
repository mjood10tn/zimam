<?php

namespace App\Agent;

use stdClass;

/**
 * One command of the agent's fixed list (§5.3). Each subclass mirrors a Go
 * params struct; SchemaTest checks it against the generated schema.
 */
abstract class AgentCommand
{
    public const REDACTED = '[redacted]';

    abstract public function name(): string;

    abstract public function kind(): AgentCommandKind;

    /** @return array<string, mixed> */
    abstract public function params(): array;

    /**
     * The params the agent marks secret, as dotted paths with * for list
     * items, such as users.*.password_hash.
     *
     * @return list<string>
     */
    public function secretFields(): array
    {
        return [];
    }

    /** A job command's RuntimeMaxSec (§5.7); null for the other kinds. */
    public function runtimeMaxSeconds(): ?int
    {
        return null;
    }

    /** Test-only commands, which only e2e builds of the agent know (D22). */
    public function e2eOnly(): bool
    {
        return false;
    }

    /** The params as they go on the wire: a JSON object, even when empty. */
    final public function jsonParams(): array|stdClass
    {
        $params = $this->params();

        return $params === [] ? new stdClass : $params;
    }

    /**
     * The params with every secret field replaced, for the panel's own log.
     *
     * @return array<string, mixed>
     */
    final public function redactedParams(): array
    {
        $params = $this->params();
        foreach ($this->secretFields() as $path) {
            $params = self::redact($params, explode('.', $path));
        }

        return $params;
    }

    /**
     * @param  list<string>  $path
     */
    private static function redact(mixed $value, array $path): mixed
    {
        if ($path === [] || ! is_array($value)) {
            return $path === [] ? self::REDACTED : $value;
        }
        $key = array_shift($path);
        foreach ($key === '*' ? array_keys($value) : [$key] as $k) {
            if (array_key_exists($k, $value)) {
                $value[$k] = self::redact($value[$k], $path);
            }
        }

        return $value;
    }
}
