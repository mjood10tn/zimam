{{-- A site's Deployments tab (R02-D2–D12). --}}
<div class="zimam-stack" @if ($polling) wire:poll.5s @endif>
    <x-filament::section :heading="__('zimam.deployments.repository.heading')">
        <form wire:submit="connect" class="zimam-stack">
            {{ $this->repositoryForm }}
            <div>
                <x-filament::button type="submit" :disabled="$site->hasUnfinishedJob()">
                    {{ $repository === null ? __('zimam.deployments.repository.connect') : __('zimam.deployments.repository.update') }}
                </x-filament::button>
            </div>
        </form>
    </x-filament::section>

    @if ($repository !== null)
        <x-filament::section :heading="__('zimam.deployments.key.heading')">
            @if ($repository->isConnected())
                <div class="grid gap-2 text-sm">
                    <p>{{ __('zimam.deployments.key.hint') }}
                        <a href="https://github.com/{{ $repository->repository }}/settings/keys" target="_blank" rel="noopener noreferrer" class="underline">{{ __('zimam.deployments.key.link') }}</a>
                    </p>
                    <pre class="overflow-x-auto whitespace-pre-wrap break-all rounded-lg bg-gray-50 p-3 font-mono text-xs dark:bg-white/5">{{ $repository->deploy_key_public }}</pre>
                    <div class="flex flex-wrap items-center gap-3">
                        <x-filament::button size="xs" color="gray" x-data x-on:click="navigator.clipboard.writeText(@js($repository->deploy_key_public))">
                            {{ __('zimam.deployments.copy') }}
                        </x-filament::button>
                        <span class="text-gray-500 dark:text-gray-400">{{ __('zimam.deployments.key.fingerprint') }} <code>{{ $repository->deploy_key_fingerprint }}</code></span>
                    </div>
                </div>
            @else
                <p class="text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.deployments.key.pending') }}</p>
            @endif
        </x-filament::section>

        <x-filament::section :heading="__('zimam.deployments.webhook.heading')">
            <div class="grid gap-3 text-sm">
                <p>{{ __('zimam.deployments.webhook.setup', ['url' => $repository->webhookUrl()]) }}</p>
                <dl class="grid gap-2">
                    <dt class="font-medium">{{ __('zimam.deployments.webhook.url') }}</dt>
                    <dd><code class="break-all">{{ $repository->webhookUrl() }}</code></dd>
                    <dt class="font-medium">{{ __('zimam.deployments.webhook.secret') }}</dt>
                    <dd>
                        @if ($revealedWebhookSecret !== null)
                            <code class="break-all">{{ $revealedWebhookSecret }}</code>
                            <p class="text-gray-500 dark:text-gray-400">{{ __('zimam.deployments.once') }}</p>
                        @else
                            <span class="text-gray-500 dark:text-gray-400">{{ __('zimam.deployments.webhook.hidden') }}</span>
                        @endif
                    </dd>
                </dl>
                <div>{{ $this->regenerateWebhookSecretAction }}</div>
            </div>
        </x-filament::section>

        <x-filament::section :heading="__('zimam.deployments.deploy_url.heading')">
            <div class="grid gap-3 text-sm">
                <p>{{ __('zimam.deployments.deploy_url.help') }}</p>
                @if ($revealedDeployToken !== null)
                    <code class="break-all">{{ $repository->deployUrl($revealedDeployToken) }}</code>
                    <p class="text-gray-500 dark:text-gray-400">{{ __('zimam.deployments.once') }}</p>
                @else
                    <span class="text-gray-500 dark:text-gray-400">{{ __('zimam.deployments.deploy_url.hidden') }}</span>
                @endif
                <div>{{ $this->regenerateDeployUrlAction }}</div>
            </div>
        </x-filament::section>

        <x-filament::section :heading="__('zimam.deployments.settings.heading')">
            <form wire:submit="saveSettings" class="zimam-stack">
                {{ $this->settingsForm }}
                <div class="flex flex-wrap gap-3">
                    <x-filament::button type="submit">{{ __('zimam.deployments.settings.save') }}</x-filament::button>
                    @if ($site->type === \App\Models\Site::LARAVEL)
                        <x-filament::button type="button" color="gray" wire:click="applyLaravelPreset">{{ __('zimam.deployments.settings.laravel_preset') }}</x-filament::button>
                    @endif
                </div>
            </form>
        </x-filament::section>

        <x-filament::section :heading="__('zimam.deployments.list.heading')">
            <div class="zimam-stack">
                <div class="flex flex-wrap items-center gap-3">
                    <x-filament::button wire:click="deployNow" :disabled="! $repository->isConnected() || $site->hasUnfinishedJob()">
                        {{ __('zimam.deployments.deploy.label') }}
                    </x-filament::button>
                    <span class="text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.deployments.deploy.help', ['branch' => $repository->branch]) }}</span>
                </div>

                @if ($deployments->isEmpty())
                    <p class="text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.deployments.list.empty') }}</p>
                @else
                    <div class="overflow-x-auto">
                        <table class="w-full text-left text-sm">
                            <thead>
                                <tr class="text-gray-500 dark:text-gray-400">
                                    <th class="py-1 pe-3 font-medium">{{ __('zimam.deployments.list.when') }}</th>
                                    <th class="py-1 pe-3 font-medium">{{ __('zimam.deployments.list.trigger') }}</th>
                                    <th class="py-1 pe-3 font-medium">{{ __('zimam.deployments.list.commit') }}</th>
                                    <th class="py-1 pe-3 font-medium">{{ __('zimam.deployments.list.release') }}</th>
                                    <th class="py-1 pe-3 font-medium">{{ __('zimam.deployments.list.status') }}</th>
                                    <th class="py-1 font-medium"><span class="sr-only">{{ __('zimam.deployments.list.log') }}</span></th>
                                </tr>
                            </thead>
                            <tbody>
                                @foreach ($deployments as $deployment)
                                    <tr class="border-t border-gray-200 dark:border-white/10" wire:key="deployment-{{ $deployment->id }}">
                                        <td class="py-2 pe-3 whitespace-nowrap">{{ \App\Support\DisplayTime::format($deployment->created_at, 'j M Y H:i') }}</td>
                                        <td class="py-2 pe-3">{{ __('zimam.deployments.triggers.'.$deployment->trigger) }}@if ($deployment->creator) · {{ $deployment->creator->name }}@endif</td>
                                        <td class="py-2 pe-3"><code>{{ $deployment->commit ? substr($deployment->commit, 0, 7) : '—' }}</code></td>
                                        <td class="py-2 pe-3"><code>{{ $deployment->release ?? '—' }}</code></td>
                                        <td class="py-2 pe-3">
                                            <x-filament::badge :color="match ($deployment->status) { 'succeeded' => 'success', 'failed' => 'danger', 'cancelled' => 'gray', default => 'warning' }">
                                                {{ __('zimam.jobs.status.'.$deployment->status) }}
                                            </x-filament::badge>
                                            @if ($deployment->error_message)
                                                <p class="mt-1 text-xs text-danger-600 dark:text-danger-400">{{ $deployment->error_message }}</p>
                                            @endif
                                        </td>
                                        <td class="py-2 text-end">
                                            <x-filament::link tag="button" wire:click="showLog({{ $deployment->id }})">{{ __('zimam.deployments.list.log') }}</x-filament::link>
                                        </td>
                                    </tr>
                                @endforeach
                            </tbody>
                        </table>
                    </div>
                @endif

                @if ($logDeploymentRow !== null)
                    <div class="grid gap-2">
                        <h3 class="text-sm font-medium">{{ __('zimam.deployments.list.log_of', ['when' => \App\Support\DisplayTime::format($logDeploymentRow->created_at, 'j M Y H:i')]) }}</h3>
                        <livewire:job-log :agent-job="$logDeploymentRow->agentJob" :key="'deployment-log-'.$logDeploymentRow->id" />
                    </div>
                @endif
            </div>
        </x-filament::section>

        @if ($repository->isConnected())
            <x-filament::section :heading="__('zimam.deployments.releases.heading')" :description="__('zimam.deployments.releases.help')">
                @if ($releases === null)
                    <p class="text-sm text-gray-500 dark:text-gray-400">{{ $releasesError ?? __('zimam.deployments.releases.unknown') }}</p>
                @elseif ($releases['releases'] === [])
                    <p class="text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.deployments.releases.empty') }}</p>
                @else
                    <ul class="grid gap-2 text-sm">
                        @foreach ($releases['releases'] as $release)
                            <li class="flex flex-wrap items-center gap-3" wire:key="release-{{ $release['id'] }}">
                                <code>{{ $release['id'] }}</code>
                                <code class="text-gray-500 dark:text-gray-400">{{ substr($release['commit'], 0, 7) }}</code>
                                @if ($release['active'])
                                    <x-filament::badge color="success">{{ __('zimam.deployments.releases.live') }}</x-filament::badge>
                                @else
                                    {{ ($this->rollbackAction)(['release' => $release['id']]) }}
                                @endif
                            </li>
                        @endforeach
                    </ul>
                @endif
            </x-filament::section>
        @endif
    @endif

    <x-filament-actions::modals />
</div>
