<?php

namespace App\Filament\Resources\SiteResource\Pages;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentClient;
use App\Agent\AgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\SiteWorkers;
use App\Agent\Commands\SiteWorkersRestart;
use App\Agent\Commands\SiteWorkersStatus;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Agent\MetricsReading;
use App\Filament\Actions\ConfirmsPassword;
use App\Models\Site;
use App\Models\SiteCredential;
use App\Models\SiteWorker;
use App\Support\Bytes;
use Closure;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\ToggleButtons;
use Filament\Notifications\Notification;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Support\Exceptions\Halt;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\View\View;
use Illuminate\Support\Collection;
use Illuminate\Support\Facades\DB;
use Livewire\Attributes\Locked;
use Livewire\Component;

/**
 * A site's Workers tab (v0.4.0, R04-D14): its queue workers with each
 * process's state from site.workers.status, add, edit and delete, restart
 * one or all. Every change submits the site's whole list as one
 * site.workers job (R04-D3); the rows are stored as submitted, marked
 * applying, and WorkerOutcomes marks them active or failed. A list over the
 * server's memory warns but still saves (R04-D18).
 */
class WorkersTab extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    #[Locked]
    public Site $site;

    /** @var array<string, list<array<string, mixed>>>|null each worker's processes, read once per request */
    private ?array $processes = null;

    /** Why the processes couldn't be read, if they couldn't. */
    private ?string $statusError = null;

    /** @var array<string, string> the colour of each process line that isn't healthy */
    private array $lineColors = [];

    public function table(Table $table): Table
    {
        return $table
            ->query($this->site->workers()->getQuery())
            ->defaultSort('id')
            ->paginated(false)
            ->emptyStateHeading(__('zimam.workers.empty'))
            ->columns([
                TextColumn::make('worker_id')
                    ->label(__('zimam.workers.columns.worker'))
                    ->weight('bold')
                    ->description(fn (SiteWorker $record): string => self::summary($record))
                    ->wrap(),
                TextColumn::make('processes')
                    ->label(__('zimam.workers.columns.processes'))
                    ->formatStateUsing(fn (int $state, SiteWorker $record): string => trans_choice('zimam.workers.processes_value', $state, ['count' => $state, 'memory' => $record->memory_mb])),
                TextColumn::make('state')
                    ->label(__('zimam.workers.columns.state'))
                    ->state(fn (SiteWorker $record): array => $this->processLines($record))
                    ->listWithLineBreaks()
                    ->color(fn (?string $state): ?string => $this->lineColors[$state] ?? null),
            ])
            ->recordActions([
                $this->guarded(
                    Action::make('edit')
                        ->label(__('zimam.workers.edit.label'))
                        ->color('gray')
                        ->modalHeading(fn (SiteWorker $record): string => __('zimam.workers.edit.heading', ['worker' => $record->worker_id]))
                        ->modalSubmitActionLabel(__('zimam.workers.edit.submit'))
                        ->fillForm(fn (SiteWorker $record): array => [
                            ...$record->only(['worker_id', 'kind', 'processes', 'memory_mb', 'connection', 'command', ...array_keys(SiteWorker::OPTIONS)]),
                            'queues' => implode(', ', $record->queues ?? []),
                        ])
                        ->schema(fn (SiteWorker $record): array => $this->workerFields($record))
                        ->action(fn (array $data, SiteWorker $record) => $this->save($data, $record)),
                ),
                $this->guarded(
                    Action::make('restart')
                        ->label(__('zimam.workers.restart.label'))
                        ->color('gray')
                        ->requiresConfirmation()
                        ->modalHeading(fn (SiteWorker $record): string => __('zimam.workers.restart.heading', ['worker' => $record->worker_id]))
                        ->modalDescription(__('zimam.workers.restart.description'))
                        ->modalSubmitActionLabel(__('zimam.workers.restart.label'))
                        ->action(fn (SiteWorker $record) => $this->restart($record->worker_id)),
                ),
                $this->guarded(
                    ConfirmsPassword::apply(
                        Action::make('delete')
                            ->label(__('zimam.workers.delete.label'))
                            ->color('danger')
                            ->modalHeading(fn (SiteWorker $record): string => __('zimam.workers.delete.heading', ['worker' => $record->worker_id]))
                            ->modalDescription(__('zimam.workers.delete.description'))
                            ->modalSubmitActionLabel(__('zimam.workers.delete.submit'))
                            ->action(fn (SiteWorker $record) => $this->submitList($this->stored()->reject(fn (SiteWorker $w): bool => $w->is($record)))),
                    ),
                ),
            ]);
    }

    public function addAction(): Action
    {
        return $this->guarded(
            Action::make('add')
                ->label(__('zimam.workers.add.label'))
                ->visible(fn (): bool => $this->site->runsPhp())
                ->modalHeading(__('zimam.workers.add.heading'))
                ->modalSubmitActionLabel(__('zimam.workers.add.submit'))
                ->schema(fn (): array => $this->workerFields(null))
                ->action(fn (array $data) => $this->save($data, null)),
            fn (): ?string => $this->site->workers()->count() >= SiteWorker::MAX_WORKERS ? __('zimam.workers.validation.too_many', ['max' => SiteWorker::MAX_WORKERS]) : null,
        );
    }

    public function restartAllAction(): Action
    {
        return $this->guarded(
            Action::make('restartAll')
                ->label(__('zimam.workers.restart.all'))
                ->color('gray')
                ->visible(fn (): bool => $this->site->runsPhp() && $this->site->workers()->exists())
                ->requiresConfirmation()
                ->modalHeading(__('zimam.workers.restart.heading_all'))
                ->modalDescription(__('zimam.workers.restart.description'))
                ->modalSubmitActionLabel(__('zimam.workers.restart.all'))
                ->action(fn () => $this->restart(null)),
        );
    }

    /** Disabled while the agent is down or the site can't take a job, with why; $blocker adds a reason. */
    private function guarded(Action $action, ?Closure $blocker = null): Action
    {
        return AgentHealth::guard($action, fn (Action $action): ?string => $this->site->jobBlocker() ?? ($blocker === null ? null : $action->evaluate($blocker)));
    }

    /**
     * The form of one worker. Fields of the other kind are hidden, so they
     * are neither checked nor kept.
     *
     * @return list<\Filament\Schemas\Components\Component>
     */
    private function workerFields(?SiteWorker $editing): array
    {
        $queue = fn (Get $get): bool => $get('kind') === SiteWorker::LARAVEL_QUEUE;
        $option = fn (string $name, string $label): TextInput => TextInput::make($name)
            ->label(__($label))
            ->integer()
            ->minValue(SiteWorker::OPTIONS[$name][0])
            ->maxValue(SiteWorker::OPTIONS[$name][1])
            ->placeholder(__('zimam.workers.fields.empty_option'))
            ->visible($queue);

        return [
            TextInput::make('worker_id')
                ->label(__('zimam.workers.fields.worker_id'))
                ->helperText(__('zimam.workers.fields.worker_id_help'))
                ->required()
                ->maxLength(16)
                ->regex(SiteWorker::ID_PATTERN)
                ->validationMessages(['regex' => __('zimam.workers.validation.worker_id')])
                ->disabled($editing !== null)
                ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail) use ($editing): void {
                    if ($editing === null && $this->site->workers()->where('worker_id', $value)->exists()) {
                        $fail(__('zimam.workers.validation.worker_id_taken'));
                    }
                }),
            ToggleButtons::make('kind')
                ->label(__('zimam.workers.fields.kind'))
                ->options(array_combine(SiteWorker::KINDS, array_map(fn (string $k): string => __("zimam.workers.fields.kinds.{$k}"), SiteWorker::KINDS)))
                ->default(SiteWorker::LARAVEL_QUEUE)
                ->inline()
                ->live()
                ->required()
                ->in(SiteWorker::KINDS),
            Grid::make(['default' => 1, 'sm' => 2])->schema([
                TextInput::make('processes')
                    ->label(__('zimam.workers.fields.processes'))
                    ->helperText(__('zimam.workers.fields.processes_help'))
                    ->required()
                    ->integer()
                    ->minValue(1)
                    ->maxValue(SiteWorker::MAX_PROCESSES)
                    ->default(1),
                TextInput::make('memory_mb')
                    ->label(__('zimam.workers.fields.memory_mb'))
                    ->helperText(__('zimam.workers.fields.memory_mb_help'))
                    ->required()
                    ->integer()
                    ->minValue(SiteWorker::MIN_MEMORY_MB)
                    ->maxValue(SiteWorker::MAX_MEMORY_MB)
                    ->default(SiteWorker::DEFAULT_MEMORY_MB),
                TextInput::make('connection')
                    ->label(__('zimam.workers.fields.connection'))
                    ->helperText(__('zimam.workers.fields.connection_help'))
                    ->required()
                    ->regex(SiteWorker::CONNECTION_PATTERN)
                    ->validationMessages(['regex' => __('zimam.workers.validation.connection')])
                    ->default(SiteWorker::DEFAULT_CONNECTION)
                    ->visible($queue),
                TextInput::make('queues')
                    ->label(__('zimam.workers.fields.queues'))
                    ->helperText(__('zimam.workers.fields.queues_help'))
                    ->visible($queue)
                    ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                        $queues = SiteWorker::queueList(is_string($value) ? $value : '');
                        if (count($queues) > SiteWorker::MAX_QUEUES) {
                            $fail(__('zimam.workers.validation.queues_max', ['max' => SiteWorker::MAX_QUEUES]));

                            return;
                        }
                        foreach ($queues as $q) {
                            if (! preg_match(SiteWorker::QUEUE_PATTERN, $q)) {
                                $fail(__('zimam.workers.validation.queue', ['queue' => $q]));

                                return;
                            }
                        }
                    }),
                $option('tries', 'zimam.workers.fields.tries'),
                $option('timeout', 'zimam.workers.fields.timeout')->helperText(__('zimam.workers.fields.timeout_help')),
                $option('sleep', 'zimam.workers.fields.sleep'),
                $option('max_time', 'zimam.workers.fields.max_time')
                    ->helperText(__('zimam.workers.fields.max_time_help'))
                    ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                        if (is_numeric($value) && (int) $value >= 1 && (int) $value < SiteWorker::MIN_MAX_TIME) {
                            $fail(__('zimam.workers.validation.max_time'));
                        }
                    }),
                $option('backoff', 'zimam.workers.fields.backoff'),
            ]),
            TextInput::make('command')
                ->label(__('zimam.workers.fields.command'))
                ->helperText(__('zimam.workers.fields.command_help'))
                ->required()
                ->visible(fn (Get $get): bool => $get('kind') === SiteWorker::CUSTOM)
                ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                    if (($error = SiteWorker::commandError(is_string($value) ? $value : '')) !== null) {
                        $fail($error);
                    }
                }),
        ];
    }

    /**
     * The site's list with $data added, or in place of $editing.
     *
     * @param  array<string, mixed>  $data  the form's state
     */
    private function save(array $data, ?SiteWorker $editing): void
    {
        $worker = new SiteWorker(self::attributesFrom($data, $editing?->worker_id));
        $list = $this->stored()->map(fn (SiteWorker $w): SiteWorker => $w->worker_id === $worker->worker_id ? $worker : $w);
        if ($editing === null) {
            $list->push($worker);
        }
        $this->submitList($list);
    }

    /**
     * A worker's columns from the form: options left empty stay null, and
     * a kind keeps only its own fields.
     *
     * @param  array<string, mixed>  $data
     * @return array<string, mixed>
     */
    private static function attributesFrom(array $data, ?string $id): array
    {
        $queue = $data['kind'] === SiteWorker::LARAVEL_QUEUE;
        $attributes = [
            'worker_id' => $id ?? $data['worker_id'],
            'kind' => $data['kind'],
            'processes' => (int) $data['processes'],
            'memory_mb' => (int) $data['memory_mb'],
            'connection' => $queue ? $data['connection'] : null,
            'queues' => $queue ? SiteWorker::queueList($data['queues'] ?? null) : null,
            'command' => $queue ? null : $data['command'],
        ];
        foreach (array_keys(SiteWorker::OPTIONS) as $option) {
            $attributes[$option] = $queue && filled($data[$option] ?? null) ? (int) $data[$option] : null;
        }

        return $attributes;
    }

    /** @return Collection<int, SiteWorker> the stored list, in the order it was saved */
    private function stored(): Collection
    {
        return $this->site->workers()->orderBy('id')->get();
    }

    /**
     * Submits $list as the site's whole list. Once the agent has it, the
     * rows become $list, each marked applying, and a worker left out is
     * marked removing: its row goes only when the job succeeds, so a failed
     * removal leaves the still-running worker on the tab to remove again.
     *
     * @param  Collection<int, SiteWorker>  $list
     */
    private function submitList(Collection $list): void
    {
        $command = new SiteWorkers($this->site->name, $list->map(fn (SiteWorker $w): array => $w->spec())->values()->all());
        if (! $this->submit($command, __('zimam.workers.refused'))) {
            return;
        }
        // Rows keep their ids, so an open row action still finds its worker.
        DB::transaction(function () use ($list): void {
            $this->site->workers()->whereNotIn('worker_id', $list->pluck('worker_id'))->update(['status' => SiteWorker::REMOVING]);
            foreach ($list as $worker) {
                $this->site->workers()->updateOrCreate(
                    ['worker_id' => $worker->worker_id],
                    [...$worker->only($worker->getFillable()), 'status' => SiteWorker::APPLYING],
                );
            }
        });
        Notification::make()->title(__('zimam.workers.submitted', ['name' => $this->site->name]))->body(__('zimam.php.submitted_body'))->success()->send();
        if (($warning = $this->memoryWarning()) !== null) {
            Notification::make()->title($warning)->warning()->send();
        }
    }

    private function restart(?string $worker): void
    {
        if ($this->submit(new SiteWorkersRestart($this->site->name, $worker), __('zimam.workers.restart.refused'))) {
            Notification::make()
                ->title($worker === null ? __('zimam.workers.restart.submitted_all') : __('zimam.workers.restart.submitted', ['worker' => $worker]))
                ->body(__('zimam.php.submitted_body'))
                ->success()
                ->send();
        }
    }

    /** Submits a job of the site's; false after saying why it wasn't taken. */
    private function submit(AgentCommand $command, string $refused): bool
    {
        if (! app(AgentHealth::class)->isReachable()) {
            Notification::make()->title(__('zimam.agent.paused'))->warning()->send();

            return false;
        }
        if (($blocker = $this->site->jobBlocker()) !== null) {
            Notification::make()->title($blocker)->warning()->send();

            return false;
        }
        try {
            app(SubmitAgentCommand::class)->run($command, $this->site);
        } catch (Halt) {
            return false;
        } catch (AgentValidationFailed $e) {
            $fields = [];
            foreach ($e->fields() as $field => $message) {
                $fields[] = "{$field}: {$message}";
            }
            Notification::make()->title($refused)->body(implode("\n", $fields))->danger()->send();

            return false;
        } catch (AgentException $e) {
            Notification::make()->title($refused)->body($e->getMessage())->danger()->send();

            return false;
        }

        return true;
    }

    /**
     * R04-D18: the most the site's workers may take, summed, when it is
     * more than the server's memory (system.metrics); null otherwise or
     * before the agent has reported it.
     */
    public function memoryWarning(): ?string
    {
        $total = MetricsReading::current()?->data['memory']['total_bytes'] ?? null;
        $ceiling = $this->stored()->where('status', '!=', SiteWorker::REMOVING)->sum(fn (SiteWorker $w): int => $w->memoryCeilingMb()) * 1024 ** 2;
        if (! is_int($total) || $total <= 0 || $ceiling <= $total) {
            return null;
        }

        return __('zimam.workers.memory_over', ['total' => Bytes::format($ceiling), 'ram' => Bytes::format($total)]);
    }

    /** What the worker runs: the queue options in short, or the command. */
    private static function summary(SiteWorker $worker): string
    {
        if ($worker->kind === SiteWorker::CUSTOM) {
            return __('zimam.workers.summary.custom', ['command' => (string) $worker->command]);
        }
        $queues = $worker->queues ?? [];

        return __('zimam.workers.summary.laravel-queue', [
            'connection' => $worker->connection ?? SiteWorker::DEFAULT_CONNECTION,
            'queues' => $queues === [] ? __('zimam.workers.summary.default_queue') : implode(', ', $queues),
        ]);
    }

    /**
     * One line per process, with the row's own state first while it isn't
     * applied. Healthy lines have no colour; failing ones are amber, failed
     * ones red, each said in words too.
     *
     * @return list<string>
     */
    private function processLines(SiteWorker $worker): array
    {
        $lines = [];
        if ($worker->status !== SiteWorker::ACTIVE) {
            $lines[] = $line = __("zimam.workers.status.{$worker->status}");
            $this->lineColors[$line] = $worker->status === SiteWorker::FAILED ? 'danger' : 'warning';
        }
        $instances = $this->processes()[$worker->worker_id] ?? [];
        if ($instances === []) {
            $lines[] = __('zimam.workers.process.none');
        }
        foreach ($instances as $p) {
            [$line, $color] = match (true) {
                $p['active'] === 'failed' => [__('zimam.workers.process.failed', ['n' => $p['instance']]), 'danger'],
                $p['failing'] => [__('zimam.workers.process.failing', ['n' => $p['instance'], 'restarts' => $p['restarts']]), 'warning'],
                $p['active'] === 'active' && $p['sub'] === 'running' => [__('zimam.workers.process.running', ['n' => $p['instance']]), null],
                default => [__('zimam.workers.process.other', ['n' => $p['instance'], 'state' => "{$p['active']} ({$p['sub']})"]), null],
            };
            $lines[] = $line;
            if ($color !== null) {
                $this->lineColors[$line] = $color;
            }
        }

        return $lines;
    }

    /**
     * site.workers.status by worker id; asked only when the site has
     * workers and the agent answers. The tab never polls: each Livewire
     * request reads it once.
     *
     * @return array<string, list<array<string, mixed>>>
     */
    private function processes(): array
    {
        if ($this->processes !== null) {
            return $this->processes;
        }
        $this->processes = [];
        if (! $this->site->runsPhp() || ! $this->site->workers()->exists() || ! app(AgentHealth::class)->isReachable()) {
            return $this->processes;
        }
        try {
            foreach (app(AgentClient::class)->call(new SiteWorkersStatus($this->site->name))->data['workers'] ?? [] as $w) {
                $this->processes[$w['id']] = $w['instances'];
            }
        } catch (AgentException $e) {
            $this->statusError = $e->getMessage();
        }

        return $this->processes;
    }

    public function render(): View
    {
        $this->processes();

        return view('filament.sites.workers-tab', [
            'statusError' => $this->statusError,
            'memoryWarning' => $this->site->runsPhp() ? $this->memoryWarning() : null,
            'horizonPrefix' => SiteCredential::usernameFor($this->site->name).':horizon:',
        ]);
    }
}
