<?php

namespace App\Filament\Pages\Backups;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\BackupPlan as BackupPlanCommand;
use App\Agent\Commands\BackupPlanDelete;
use App\Agent\Commands\BackupRun;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentException;
use App\Filament\Actions\ConfirmsPassword;
use App\Models\BackupDestination;
use App\Models\BackupPlan;
use App\Models\Database;
use App\Models\Site;
use App\Support\DisplayTime;
use Closure;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Notifications\Notification;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Support\Exceptions\Halt;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\View\View;
use Illuminate\Support\Facades\DB;
use Livewire\Component;

/**
 * The Backups page's plans: create and edit one (what, when, how many to
 * keep, where to), run it now, delete it. The rows are the panel's editing
 * copy; each save sends the whole plan with backup.plan.
 */
class PlansTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public function createAction(): Action
    {
        return AgentHealth::guard(
            Action::make('create')
                ->label(__('zimam.backups.plans.create'))
                ->modalHeading(__('zimam.backups.plans.create'))
                ->modalSubmitActionLabel(__('zimam.backups.plans.save.submit'))
                ->fillForm([
                    'include_panel' => false,
                    'frequency' => 'daily',
                    'time' => '02:00',
                    'day' => 'sun',
                    'keep_last' => BackupPlan::DEFAULT_KEEP_LAST,
                    'keep_daily' => 0,
                    'keep_weekly' => 0,
                    'keep_monthly' => 0,
                ])
                ->schema(self::fields(null))
                ->action(fn (array $data) => $this->save(new BackupPlan(['key' => $data['key']]), $data)),
            fn (): ?string => BackupDestination::where('status', BackupDestination::ACTIVE)->exists() ? null : __('zimam.backups.plans.no_destination'),
        );
    }

    /** @return list<\Filament\Schemas\Components\Component> */
    private static function fields(?BackupPlan $plan): array
    {
        $timed = fn (Get $get): bool => $get('frequency') !== 'hourly';
        $nothingElse = fn (Get $get): bool => ! $get('include_panel') && empty($get('databases'));
        $keep = fn (string $name, int $min): TextInput => TextInput::make($name)
            ->label(__("zimam.backups.plans.fields.{$name}"))
            ->integer()->minValue($min)->maxValue(BackupPlan::MAX_KEEP)
            ->required();

        return [
            TextInput::make('key')
                ->label(__('zimam.backups.plans.fields.key'))
                ->helperText(__('zimam.backups.key_help'))
                ->required()
                ->disabled($plan !== null)
                ->regex(BackupDestination::KEY_PATTERN)
                ->unique(BackupPlan::class, 'key', ignoreRecord: true)
                ->validationMessages(['regex' => __('zimam.backups.validation.key'), 'unique' => __('zimam.backups.validation.key_taken')]),
            Select::make('destination')
                ->label(__('zimam.backups.plans.fields.destination'))
                ->options(fn (): array => BackupDestination::where('status', BackupDestination::ACTIVE)->orderBy('key')->pluck('key', 'id')->all())
                ->required(),
            Select::make('sites')
                ->label(__('zimam.backups.plans.fields.sites'))
                ->helperText(__('zimam.backups.plans.fields.sites_help'))
                ->multiple()
                ->options(fn (): array => Site::where('status', Site::ACTIVE)->orderBy('name')->pluck('name', 'id')->all())
                ->maxItems(BackupPlan::MAX_SITES)
                ->required($nothingElse)
                ->validationMessages(['required' => __('zimam.backups.validation.nothing')]),
            Select::make('databases')
                ->label(__('zimam.backups.plans.fields.databases'))
                ->multiple()
                ->options(fn (): array => Database::where('status', Database::ACTIVE)->orderBy('name')->pluck('name', 'id')->all())
                ->maxItems(BackupPlan::MAX_DATABASES)
                ->live(),
            Toggle::make('include_panel')
                ->label(__('zimam.backups.plans.fields.include_panel'))
                ->helperText(__('zimam.backups.plans.fields.include_panel_help'))
                ->live(),
            Select::make('frequency')
                ->label(__('zimam.backups.plans.fields.frequency'))
                ->options(['hourly' => __('zimam.backups.schedule.hourly'), 'daily' => __('zimam.backups.schedule.daily'), 'weekly' => __('zimam.backups.schedule.weekly')])
                ->required()
                ->live(),
            Select::make('day')
                ->label(__('zimam.backups.plans.fields.day'))
                ->options(array_combine(BackupPlan::DAYS, array_map(fn (string $d): string => __("zimam.backups.schedule.days.{$d}"), BackupPlan::DAYS)))
                ->visible(fn (Get $get): bool => $get('frequency') === 'weekly')
                ->required(fn (Get $get): bool => $get('frequency') === 'weekly'),
            TextInput::make('time')
                ->label(__('zimam.backups.plans.fields.time'))
                ->helperText(__('zimam.backups.plans.fields.time_help'))
                ->visible($timed)
                ->required($timed)
                ->regex(BackupPlan::TIME_PATTERN)
                ->validationMessages(['regex' => __('zimam.backups.validation.time')]),
            $keep('keep_last', 1),
            $keep('keep_daily', 0),
            $keep('keep_weekly', 0),
            $keep('keep_monthly', 0),
        ];
    }

    /** @param  array<string, mixed>  $data */
    private static function schedule(array $data): string
    {
        return match ($data['frequency']) {
            'daily' => "daily {$data['time']}",
            'weekly' => "weekly {$data['day']} {$data['time']}",
            default => 'hourly',
        };
    }

    /**
     * Saves the plan's rows and sends it whole. A refusal puts a new plan
     * away and an edited one back as it was.
     *
     * @param  array<string, mixed>  $data
     */
    private function save(BackupPlan $plan, array $data): void
    {
        $new = ! $plan->exists;
        $before = $new ? null : [$plan->only(['backup_destination_id', 'schedule', 'retention', 'include_panel', 'status', 'last_error']), $plan->sites()->pluck('sites.id')->all(), $plan->databases()->pluck('databases.id')->all()];
        $retention = ['keep_last' => (int) $data['keep_last'], 'keep_daily' => (int) ($data['keep_daily'] ?? 0), 'keep_weekly' => (int) ($data['keep_weekly'] ?? 0), 'keep_monthly' => (int) ($data['keep_monthly'] ?? 0)];
        DB::transaction(function () use ($plan, $data, $retention): void {
            $plan->fill([
                'backup_destination_id' => $data['destination'],
                'schedule' => self::schedule($data),
                'retention' => $retention,
                'include_panel' => (bool) ($data['include_panel'] ?? false),
                'status' => BackupPlan::SAVING,
                'last_error' => null,
            ])->save();
            $plan->sites()->sync($data['sites'] ?? []);
            $plan->databases()->sync($data['databases'] ?? []);
        });
        $command = new BackupPlanCommand(
            $plan->key,
            $plan->destination->key,
            $plan->sites()->orderBy('name')->pluck('name')->all(),
            $plan->databases()->orderBy('name')->pluck('name')->all(),
            $plan->include_panel,
            $plan->schedule,
            $retention,
        );
        $undo = function () use ($plan, $before): void {
            if ($before === null) {
                $plan->delete();

                return;
            }
            $plan->update($before[0]);
            $plan->sites()->sync($before[1]);
            $plan->databases()->sync($before[2]);
        };
        $this->submit($command, $plan, __('zimam.backups.plans.save.submitted', ['key' => $plan->key]), $undo);
    }

    public function table(Table $table): Table
    {
        $idle = fn (BackupPlan $record): ?string => match (true) {
            $record->status === BackupPlan::DELETING || $record->status === BackupPlan::SAVING => __('zimam.backups.working'),
            $record->hasUnfinishedJob() => __('zimam.backups.working'),
            default => null,
        };

        return $table
            ->query(BackupPlan::query()->with(['destination', 'sites', 'databases']))
            ->defaultSort('key')
            ->paginated(false)
            ->emptyStateHeading(__('zimam.backups.plans.empty'))
            ->columns([
                TextColumn::make('key')
                    ->label(__('zimam.backups.plans.columns.name'))
                    ->weight('bold')
                    ->description(fn (BackupPlan $record): string => __('zimam.backups.plans.to', ['destination' => $record->destination->key])),
                TextColumn::make('contents')
                    ->label(__('zimam.backups.plans.columns.contents'))
                    ->state(fn (BackupPlan $record): array => [
                        ...$record->sites->pluck('name')->map(fn (string $n): string => __('zimam.backups.plans.site_files', ['name' => $n])),
                        ...$record->databases->pluck('name'),
                        ...($record->include_panel ? [__('zimam.backups.plans.panel_database')] : []),
                    ])
                    ->listWithLineBreaks(),
                TextColumn::make('schedule')
                    ->label(__('zimam.backups.plans.columns.schedule'))
                    ->formatStateUsing(fn (BackupPlan $record): string => $record->scheduleLabel())
                    ->description(fn (BackupPlan $record): string => __('zimam.backups.plans.keeps', $record->retention + ['keep_daily' => 0, 'keep_weekly' => 0, 'keep_monthly' => 0])),
                TextColumn::make('status')
                    ->label(__('zimam.backups.plans.columns.status'))
                    ->badge()
                    ->formatStateUsing(fn (string $state): string => __("zimam.backups.status.{$state}"))
                    ->color(fn (string $state): string => match ($state) {
                        BackupPlan::FAILED => 'danger',
                        BackupPlan::ACTIVE => 'gray',
                        default => 'warning',
                    })
                    ->description(fn (BackupPlan $record): ?string => $record->last_error),
                TextColumn::make('last_run')
                    ->label(__('zimam.backups.plans.columns.last_run'))
                    ->state(function (BackupPlan $record): string {
                        $run = $record->runs()->latest('backup_id')->first();

                        return $run === null ? __('zimam.backups.plans.never') : __("zimam.backups.runs.status.{$run->status}").' · '.DisplayTime::format($run->started_at, 'j M H:i');
                    })
                    ->alignEnd(),
            ])
            ->recordActions([
                AgentHealth::guard(
                    Action::make('run')
                        ->label(__('zimam.backups.plans.run.label'))
                        ->action(fn (BackupPlan $record) => $this->runNow($record)),
                    fn (BackupPlan $record): ?string => $record->status === BackupPlan::ACTIVE ? $idle($record) : __('zimam.backups.plans.not_ready'),
                ),
                AgentHealth::guard(
                    Action::make('edit')
                        ->label(__('zimam.backups.plans.edit'))
                        ->color('gray')
                        ->modalHeading(fn (BackupPlan $record): string => __('zimam.backups.plans.edit_heading', ['key' => $record->key]))
                        ->modalSubmitActionLabel(__('zimam.backups.plans.save.submit'))
                        ->fillForm(fn (BackupPlan $record): array => self::formData($record))
                        ->schema(fn (BackupPlan $record): array => self::fields($record))
                        ->action(fn (BackupPlan $record, array $data) => $this->save($record, $data)),
                    $idle,
                ),
                AgentHealth::guard(
                    ConfirmsPassword::apply(
                        Action::make('delete')
                            ->label(__('zimam.backups.plans.delete.label'))
                            ->color('danger')
                            ->modalHeading(fn (BackupPlan $record): string => __('zimam.backups.plans.delete.heading', ['key' => $record->key]))
                            ->modalDescription(__('zimam.backups.plans.delete.description'))
                            ->modalSubmitActionLabel(__('zimam.backups.plans.delete.submit'))
                            ->action(fn (BackupPlan $record) => $this->delete($record)),
                    ),
                    $idle,
                ),
            ]);
    }

    /** @return array<string, mixed> the edit form, filled from the plan */
    private static function formData(BackupPlan $plan): array
    {
        $f = explode(' ', $plan->schedule);

        return [
            'key' => $plan->key,
            'destination' => $plan->backup_destination_id,
            'sites' => $plan->sites()->pluck('sites.id')->all(),
            'databases' => $plan->databases()->pluck('databases.id')->all(),
            'include_panel' => $plan->include_panel,
            'frequency' => $f[0],
            'day' => $f[0] === 'weekly' ? $f[1] : 'sun',
            'time' => $f[0] === 'hourly' ? '02:00' : end($f),
            'keep_last' => $plan->retention['keep_last'],
            'keep_daily' => $plan->retention['keep_daily'] ?? 0,
            'keep_weekly' => $plan->retention['keep_weekly'] ?? 0,
            'keep_monthly' => $plan->retention['keep_monthly'] ?? 0,
        ];
    }

    /** A run already under way, from the timer too, is refused rather than queued behind itself. */
    private function runNow(BackupPlan $plan): void
    {
        try {
            app(SubmitAgentCommand::class)->run(new BackupRun($plan->key), $plan, waitWhenBusy: false);
        } catch (Halt) {
            return;
        } catch (AgentBusy) {
            Notification::make()->title(__('zimam.backups.plans.run.busy'))->warning()->send();

            return;
        } catch (AgentException $e) {
            Notification::make()->title(DestinationsTable::reason($e))->danger()->send();

            return;
        }
        Notification::make()->title(__('zimam.backups.plans.run.submitted', ['key' => $plan->key]))->body(__('zimam.backups.submitted_body'))->success()->send();
    }

    private function delete(BackupPlan $plan): void
    {
        $before = $plan->only(['status', 'last_error']);
        $plan->update(['status' => BackupPlan::DELETING, 'last_error' => null]);
        $this->submit(new BackupPlanDelete($plan->key), $plan, __('zimam.backups.plans.delete.submitted', ['key' => $plan->key]), fn () => $plan->update($before));
    }

    private function submit(AgentCommand $command, BackupPlan $plan, string $submitted, Closure $undo): void
    {
        try {
            app(SubmitAgentCommand::class)->run($command, $plan);
        } catch (Halt $e) {
            $undo();

            throw $e;
        } catch (AgentException $e) {
            $undo();
            Notification::make()->title(DestinationsTable::reason($e))->danger()->send();

            return;
        }
        Notification::make()->title($submitted)->body(__('zimam.backups.submitted_body'))->success()->send();
    }

    public function render(): View
    {
        return view('filament.backups.plans');
    }
}
