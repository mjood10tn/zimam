<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;
use SensitiveParameter;

/**
 * Sets up a backup destination: its rclone remotes and, for SFTP, its key
 * pair (R05-D2). The S3 credentials and the encryption keys are secrets:
 * redacted wherever the panel records the command, and never queued
 * (R05-D3). A new destination needs both keys; the panel never has them
 * again, so it sends them only once.
 */
final class BackupDestination extends AgentCommand
{
    /**
     * @param  array<string, mixed>  $settings  the s3 or sftp fields, credentials included
     */
    public function __construct(
        public readonly string $id,
        public readonly string $type,
        #[SensitiveParameter] private readonly array $settings,
        #[SensitiveParameter] private readonly ?string $encryptionPassword = null,
        #[SensitiveParameter] private readonly ?string $encryptionSalt = null,
    ) {}

    public function name(): string
    {
        return 'backup.destination';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        $params = ['id' => $this->id, 'type' => $this->type, $this->type => $this->settings];
        if ($this->encryptionPassword !== null) {
            $params['encryption_password'] = $this->encryptionPassword;
            $params['encryption_salt'] = $this->encryptionSalt;
        }

        return $params;
    }

    public function secretFields(): array
    {
        return ['s3.access_key_id', 's3.secret_access_key', 'encryption_password', 'encryption_salt'];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 300;
    }
}
