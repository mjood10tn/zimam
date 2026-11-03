<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Creates a site, or continues one whose provisioning failed (§9.2). A static site sends no php_version. */
final class SiteProvision extends AgentCommand
{
    public function __construct(
        public readonly string $siteName,
        public readonly string $domain,
        public readonly string $type,
        public readonly ?string $phpVersion = null,
    ) {}

    public function name(): string
    {
        return 'site.provision';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        $params = ['name' => $this->siteName, 'domain' => $this->domain, 'type' => $this->type];
        if ($this->phpVersion !== null) {
            $params['php_version'] = $this->phpVersion;
        }

        return $params;
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 300;
    }
}
