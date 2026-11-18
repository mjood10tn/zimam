<?php

namespace App\Filament\Resources\SiteResource\Pages;

use App\Agent\Commands\SiteCron;
use App\Agent\ServerStatus;
use App\Filament\Actions\ConfirmsPassword;
use App\Filament\Concerns\SubmitsSiteJobs;
use App\Models\Site;
use App\Models\SiteCronJob;
use App\Support\CronSchedule;
use Closure;
use DateTimeImmutable;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Notifications\Notification;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Enums\RecordActionsPosition;
use Filament\Tables\Table;
use Illuminate\Contracts\View\View;
use Illuminate\Support\Collection;
use Livewire\Attributes\Locked;
use Livewire\Component;

/**
 * A site's Schedule tab (v0.4.0, R04-D5, R04-D14): the Laravel scheduler's
 * switch and the owner's cron jobs, each with its next five runs as plain
 * text in the server's zone (ruling R7). Every change submits the scheduler
 * flag and the site's whole list as one site.cron job; the rows are stored as
 * submitted, marked applying, and CronOutcomes marks them active or failed.
 * It shares the Workers tab's submit and removal handling (SubmitsSiteJobs).
 */
class ScheduleTab extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;
    use SubmitsSiteJobs;

    /** How many upcoming runs a job shows. */
    private const PREVIEW_RUNS = 5;

    #[Locked]
    public Site $site;

    private ?string $zone = null;

    /** @var array<string, string> the colour of each state line that isn't plain */
    private array $lineColors = [];

    public function table(Table $table): Table
    {
        return $table
            ->query($this->site->cronJobs()->getQuery())
            ->defaultSort('id')
            ->paginated(false)
            ->emptyStateHeading(__('zimam.schedule.empty'))
            ->columns([
                TextColumn::make('job_id')
                    ->label(__('zimam.schedule.columns.job'))
                    ->weight('bold')
                    ->description(fn (SiteCronJob $record): string => $record->command)
                    ->wrap(),
                TextColumn::make('schedule')
                    ->label(__('zimam.schedule.columns.schedule')),
                TextColumn::make('next')
                    ->label(fn (): string => __('zimam.schedule.columns.next', ['zone' => $this->zone()]))
                    ->state(fn (SiteCronJob $record): array => $this->nextLines($record))
                    ->listWithLineBreaks()
                    ->alignEnd()
                    ->color(fn (?string $state): ?string => $this->lineColors[$state] ?? null),
            ])
            // Edit and Delete lead the row, so the next runs are the trailing,
            // right-aligned column (DESIGN.md, The One Age Column Rule).
            ->recordActions([
                $this->guarded(
                    Action::make('edit')
                        ->label(__('zimam.schedule.edit.label'))
                        ->color('gray')
                        ->modalHeading(fn (SiteCronJob $record): string => __('zimam.schedule.edit.heading', ['job' => $record->job_id]))
                        ->modalSubmitActionLabel(__('zimam.schedule.edit.submit'))
                        ->fillForm(fn (SiteCronJob $record): array => $record->only(['job_id', 'schedule', 'command', 'enabled']))
                        ->schema(fn (SiteCronJob $record): array => $this->jobFields($record))
                        ->action(fn (array $data, SiteCronJob $record) => $this->save($data, $record)),
                ),
                $this->guarded(
                    ConfirmsPassword::apply(
                        Action::make('delete')
                            ->label(__('zimam.schedule.delete.label'))
                            ->color('danger')
                            ->modalHeading(fn (SiteCronJob $record): string => __('zimam.schedule.delete.heading', ['job' => $record->job_id]))
                            ->modalDescription(__('zimam.schedule.delete.description'))
                            ->modalSubmitActionLabel(__('zimam.schedule.delete.submit'))
                            ->action(fn (SiteCronJob $record) => $this->submitList($this->stored()->reject(fn (SiteCronJob $j): bool => $j->is($record)))),
                    ),
                ),
            ], position: RecordActionsPosition::BeforeColumns);
    }

    public function addAction(): Action
    {
        return $this->guarded(
            Action::make('add')
                ->label(__('zimam.schedule.add.label'))
                ->modalHeading(__('zimam.schedule.add.heading'))
                ->modalSubmitActionLabel(__('zimam.schedule.add.submit'))
                ->schema(fn (): array => $this->jobFields(null))
                ->action(fn (array $data) => $this->save($data, null)),
            fn (): ?string => $this->site->cronJobs()->count() >= SiteCronJob::MAX_JOBS ? __('zimam.schedule.validation.too_many', ['max' => SiteCronJob::MAX_JOBS]) : null,
        );
    }

    /** The scheduler's switch: a static site has no PHP, so no scheduler (the agent refuses it with conflict). */
    public function schedulerAction(): Action
    {
        return $this->guarded(
            Action::make('scheduler')
                ->label(fn (): string => $this->site->scheduler_enabled ? __('zimam.schedule.scheduler.turn_off') : __('zimam.schedule.scheduler.turn_on'))
                ->color('gray')
                ->visible(fn (): bool => $this->site->runsPhp())
                ->action(function (): void {
                    $on = ! $this->site->scheduler_enabled;
                    $this->submitList($this->stored(), $on, __($on ? 'zimam.schedule.scheduler.submitted_on' : 'zimam.schedule.scheduler.submitted_off', ['name' => $this->site->name]));
                }),
        );
    }

    /** @return list<\Filament\Schemas\Components\Component> */
    private function jobFields(?SiteCronJob $editing): array
    {
        return [
            TextInput::make('job_id')
                ->label(__('zimam.schedule.fields.job_id'))
                ->helperText(__('zimam.schedule.fields.job_id_help'))
                ->required()
                ->maxLength(16)
                ->regex(SiteCronJob::ID_PATTERN)
                ->validationMessages(['regex' => __('zimam.schedule.validation.job_id')])
                ->disabled($editing !== null)
                ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail) use ($editing): void {
                    if ($value === SiteCronJob::RESERVED_ID) {
                        $fail(__('zimam.schedule.validation.job_id_reserved'));
                    } elseif ($editing === null && $this->site->cronJobs()->where('job_id', $value)->exists()) {
                        $fail(__('zimam.schedule.validation.job_id_taken'));
                    }
                }),
            TextInput::make('schedule')
                ->label(__('zimam.schedule.fields.schedule'))
                ->helperText(__('zimam.schedule.fields.schedule_help'))
                ->required()
                ->trim()
                ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                    if (($error = CronSchedule::error(is_string($value) ? $value : '')) !== null) {
                        $fail($error);
                    }
                }),
            TextInput::make('command')
                ->label(__('zimam.schedule.fields.command'))
                ->helperText(__('zimam.schedule.fields.command_help'))
                ->required()
                ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                    if (($error = SiteCronJob::commandError(is_string($value) ? $value : '')) !== null) {
                        $fail($error);
                    }
                }),
            Toggle::make('enabled')
                ->label(__('zimam.schedule.fields.enabled'))
                ->default(true),
        ];
    }

    /**
     * The site's list with $data added, or in place of $editing.
     *
     * @param  array<string, mixed>  $data  the form's state
     */
    private function save(array $data, ?SiteCronJob $editing): void
    {
        $job = new SiteCronJob([
            'job_id' => $editing?->job_id ?? $data['job_id'],
            'schedule' => $data['schedule'],
            'command' => $data['command'],
            'enabled' => (bool) $data['enabled'],
        ]);
        $list = $this->stored()->map(fn (SiteCronJob $j): SiteCronJob => $j->job_id === $job->job_id ? $job : $j);
        if ($editing === null) {
            $list->push($job);
        }
        $this->submitList($list);
    }

    /** @return Collection<int, SiteCronJob> the stored list, in the order it was saved */
    private function stored(): Collection
    {
        return $this->site->cronJobs()->orderBy('id')->get();
    }

    /**
     * Submits $list as the site's whole list. Once the agent has it, the
     * rows become $list, each marked applying, and a job left out is marked
     * removing: its row goes only when the job succeeds, so a failed removal
     * leaves the still-scheduled job on the tab to remove again. The scheduler
     * keeps the state the site has unless $scheduler says otherwise.
     *
     * @param  Collection<int, SiteCronJob>  $list
     */
    private function submitList(Collection $list, ?bool $scheduler = null, ?string $title = null): void
    {
        $command = new SiteCron($this->site->name, $scheduler ?? $this->site->scheduler_enabled, $list->map(fn (SiteCronJob $j): array => $j->spec())->values()->all());
        if (! $this->submit($command, __('zimam.schedule.refused'))) {
            return;
        }
        $this->storeSubmittedList('cronJobs', 'job_id', $list, SiteCronJob::APPLYING, SiteCronJob::REMOVING);
        Notification::make()->title($title ?? __('zimam.schedule.submitted', ['name' => $this->site->name]))->body(__('zimam.php.submitted_body'))->success()->send();
    }

    /** The zone cron runs in, read once per request; UTC until the agent has said. */
    private function zone(): string
    {
        return $this->zone ??= ServerStatus::current()?->timezone() ?? 'UTC';
    }

    /**
     * What the job's Next runs cell says: its own state first while it isn't
     * applied, then the next runs, or that it is paused. Failing states are
     * red, applying ones amber, each said in words too.
     *
     * @return list<string>
     */
    private function nextLines(SiteCronJob $job): array
    {
        $lines = [];
        if ($job->status !== SiteCronJob::ACTIVE) {
            $lines[] = $line = __("zimam.schedule.status.{$job->status}");
            $this->lineColors[$line] = $job->status === SiteCronJob::FAILED ? 'danger' : 'warning';
        }
        if (! $job->enabled) {
            return [...$lines, __('zimam.schedule.paused')];
        }
        $runs = CronSchedule::parse($job->schedule)->nextRuns(self::PREVIEW_RUNS, now()->setTimezone($this->zone()));

        return [...$lines, ...($runs === [] ? [__('zimam.schedule.never')] : array_map(fn (DateTimeImmutable $at): string => $at->format('D j M Y, H:i'), $runs))];
    }

    public function render(): View
    {
        return view('filament.sites.schedule-tab', ['zone' => $this->zone()]);
    }
}
