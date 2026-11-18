<?php

namespace App\Filament\Pages;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\NodeInstall;
use App\Agent\Commands\NodeUninstall;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Agent\ServerStatus;
use App\Filament\Actions\ConfirmsPassword;
use App\Models\AgentJob;
use BackedEnum;
use Carbon\CarbonImmutable;
use Filament\Actions\Action;
use Filament\Forms\Components\Select;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Schema;
use Filament\Support\Exceptions\Halt;
use Filament\Support\Icons\Heroicon;

/**
 * Node.js: install a major, update it, change the major or uninstall (R04-D6,
 * R04-D19). Update is the installed major submitted again. Nothing is stored:
 * system.status's `node` is the truth (ledger R1), and an unfinished node.*
 * job is what says a change is under way.
 */
class Node extends Page
{
    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedCommandLine;

    protected static ?int $navigationSort = 4;

    public static function getNavigationLabel(): string
    {
        return __('zimam.node.title');
    }

    public function getTitle(): string
    {
        return __('zimam.node.title');
    }

    /**
     * What the agent says of Node, or null when it says nothing: an older
     * agent, or one that couldn't read it. Then nothing is offered and
     * "not installed" is not claimed (docs/STATUS.md, unknown vs absent).
     *
     * @return array{installed: bool, major?: string, version?: string, available: list<array{major: string, end_of_life: string, supported: bool, recommended: bool}>}|null
     */
    private function node(): ?array
    {
        return ServerStatus::current()?->data['node'] ?? null;
    }

    /**
     * The Node version in a Debian package version: `26.13.1` from
     * `26.13.1-1nodesource1`, without an epoch or the Debian revision. The
     * agent's field stays the package version; only the display drops them.
     */
    public static function displayVersion(string $package): string
    {
        return preg_match('/\A(?:\d+:)?(\d+(?:\.\d+)*)/', $package, $m) === 1 ? $m[1] : $package;
    }

    private function installedMajor(): ?string
    {
        $node = $this->node();

        return ($node['installed'] ?? false) ? ($node['major'] ?? null) : null;
    }

    private function working(): bool
    {
        return AgentJob::whereIn('command', ['node.install', 'node.uninstall'])->whereNotIn('status', AgentJob::FINISHED)->exists();
    }

    /** @return array{major: string, end_of_life: string, supported: bool, recommended: bool}|null */
    private function release(string $major): ?array
    {
        return array_column($this->node()['available'] ?? [], null, 'major')[$major] ?? null;
    }

    /**
     * The majors the forms offer: those the agent lists as supported today,
     * less $except; a past-EOL one can't be installed (the agent refuses it).
     *
     * @return array<string, string> major => label
     */
    public function offeredMajors(?string $except = null): array
    {
        $majors = [];
        foreach ($this->node()['available'] ?? [] as $release) {
            if ($release['supported'] && $release['major'] !== $except) {
                $majors[$release['major']] = __('zimam.node.install.option', [
                    'major' => $release['major'],
                    'end' => CarbonImmutable::parse($release['end_of_life'])->translatedFormat('M j, Y'),
                ]);
            }
        }

        return $majors;
    }

    private function recommended(?string $except = null): ?string
    {
        foreach ($this->node()['available'] ?? [] as $release) {
            if ($release['recommended'] && $release['supported'] && $release['major'] !== $except) {
                return $release['major'];
            }
        }

        return array_key_first($this->offeredMajors($except));
    }

    public function content(Schema $schema): Schema
    {
        return $schema->components([
            Text::make(function (): string {
                if ($this->node() === null) {
                    return __('zimam.node.unknown');
                }
                $major = $this->installedMajor();
                if ($major === null) {
                    return __('zimam.node.not_installed');
                }
                $text = __('zimam.node.installed', ['version' => self::displayVersion($this->node()['version'] ?? $major), 'major' => $major]);
                if ($release = $this->release($major)) {
                    $date = CarbonImmutable::parse($release['end_of_life'])->translatedFormat('M j, Y');
                    $text .= ' '.__($release['supported'] ? 'zimam.node.ends' : 'zimam.node.ended', ['major' => $major, 'date' => $date]);
                }

                return $text;
            }),
            Text::make(__('zimam.node.working'))->visible(fn (): bool => $this->working()),
        ]);
    }

    protected function getHeaderActions(): array
    {
        $idle = fn (): bool => $this->node() !== null && ! $this->working();

        return [
            AgentHealth::guard(
                Action::make('install')
                    ->label(__('zimam.node.install.label'))
                    ->visible(fn (): bool => $idle() && $this->installedMajor() === null)
                    ->schema(fn (): array => [
                        Select::make('major')
                            ->label(__('zimam.node.install.major'))
                            ->options($this->offeredMajors())
                            ->default($this->recommended())
                            ->noOptionsMessage(__('zimam.node.install.none'))
                            ->required(),
                    ])
                    ->modalSubmitActionLabel(__('zimam.node.install.submit'))
                    ->action(fn (array $data) => $this->submit(new NodeInstall($data['major']), 'install', ['major' => $data['major']])),
            ),
            AgentHealth::guard(
                Action::make('update')
                    ->label(__('zimam.node.update.label'))
                    ->color('gray')
                    ->visible(fn (): bool => $idle() && $this->installedMajor() !== null && ($this->release($this->installedMajor())['supported'] ?? false))
                    ->requiresConfirmation()
                    ->modalHeading(__('zimam.node.update.heading'))
                    ->modalDescription(fn (): string => __('zimam.node.update.description', ['major' => $this->installedMajor()]))
                    ->modalSubmitActionLabel(__('zimam.node.update.submit'))
                    ->action(fn () => $this->submit(new NodeInstall($this->installedMajor()), 'update', ['major' => $this->installedMajor()])),
            ),
            // Another major purges the installed one first, so it is as destructive as uninstall.
            AgentHealth::guard(
                ConfirmsPassword::apply(
                    Action::make('change')
                        ->label(__('zimam.node.change.label'))
                        ->color('danger')
                        ->visible(fn (): bool => $idle() && $this->installedMajor() !== null && $this->offeredMajors($this->installedMajor()) !== [])
                        ->modalHeading(__('zimam.node.change.heading'))
                        ->modalDescription(__('zimam.node.change.description'))
                        ->modalSubmitActionLabel(__('zimam.node.change.submit'))
                        ->action(fn (array $data) => $this->submit(new NodeInstall($data['major']), 'change', ['major' => $data['major']])),
                    [
                        Select::make('major')
                            ->label(__('zimam.node.change.major'))
                            ->options(fn (): array => $this->offeredMajors($this->installedMajor()))
                            ->default(fn (): ?string => $this->recommended($this->installedMajor()))
                            ->required(),
                    ],
                ),
            ),
            AgentHealth::guard(
                ConfirmsPassword::apply(
                    Action::make('uninstall')
                        ->label(__('zimam.node.uninstall.label'))
                        ->color('danger')
                        ->visible(fn (): bool => $idle() && $this->installedMajor() !== null)
                        ->modalHeading(__('zimam.node.uninstall.heading'))
                        ->modalDescription(__('zimam.node.uninstall.description'))
                        ->modalSubmitActionLabel(__('zimam.node.uninstall.label'))
                        ->action(fn () => $this->submit(new NodeUninstall, 'uninstall')),
                ),
            ),
        ];
    }

    /**
     * Submits a node.* job. A refusal says why in the agent's words (a Node
     * past its end of life, or a site job that makes uninstall busy); an
     * unreachable agent halts, after SubmitAgentCommand has told the user.
     *
     * @param  array<string, string>  $replace
     */
    private function submit(AgentCommand $command, string $action, array $replace = []): void
    {
        try {
            app(SubmitAgentCommand::class)->run($command);
        } catch (Halt $e) {
            throw $e;
        } catch (AgentException $e) {
            Notification::make()
                ->title($e instanceof AgentValidationFailed && $e->fields() !== [] ? implode(' ', $e->fields()) : $e->getMessage())
                ->danger()
                ->send();

            return;
        }
        Notification::make()
            ->title(__("zimam.node.{$action}.submitted", $replace))
            ->body(__('zimam.node.submitted_body'))
            ->success()
            ->send();
    }
}
