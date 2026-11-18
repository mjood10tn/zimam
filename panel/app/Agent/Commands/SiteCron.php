<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Sets a site's Laravel scheduler and whole list of cron jobs (v0.4.0,
 * R04-D5): the agent makes the cron file and scripts match, so a job left out
 * is removed and an empty list removes them all. A static site takes jobs but
 * not the scheduler (conflict).
 */
final class SiteCron extends AgentCommand
{
    /** @param list<array<string, mixed>> $jobs CronJobSpecs, as SiteCronJob::spec() builds them */
    public function __construct(
        public readonly string $site,
        public readonly bool $scheduler,
        public readonly array $jobs,
    ) {}

    public function name(): string
    {
        return 'site.cron';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['name' => $this->site, 'scheduler' => $this->scheduler, 'jobs' => $this->jobs];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 120;
    }
}
