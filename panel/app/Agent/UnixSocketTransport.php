<?php

namespace App\Agent;

use App\Agent\Exceptions\AgentUnavailable;

/** The agent's socket, /run/zimam/agent.sock on a server (§5.1). */
final class UnixSocketTransport implements AgentTransport
{
    /**
     * "No read timeout" in seconds: a day, longer than any job's limit. A
     * stream otherwise inherits default_socket_timeout (60 s), which would
     * end follow() during a job's silent stretch.
     */
    private const NO_READ_TIMEOUT = 86400;

    public function __construct(
        private readonly string $path,
        private readonly float $connectTimeout = 2.0,
    ) {}

    public function send(string $line, ?int $readTimeout)
    {
        $socket = @stream_socket_client('unix://'.$this->path, $errno, $error, $this->connectTimeout);
        if ($socket === false) {
            throw new AgentUnavailable("Cannot reach the agent at {$this->path}: {$error}");
        }
        stream_set_timeout($socket, $readTimeout ?? self::NO_READ_TIMEOUT);
        // A socket may take only part of a write.
        for ($written = 0; $written < strlen($line); $written += $n) {
            $n = @fwrite($socket, substr($line, $written));
            if ($n === false || $n === 0) {
                fclose($socket);
                throw new AgentUnavailable("Cannot send the request to the agent at {$this->path}");
            }
        }

        return $socket;
    }
}
