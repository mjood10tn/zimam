<?php

namespace App\Filament\Resources;

use App\Filament\Resources\AgentJobResource\Pages\ListAgentJobs;
use App\Filament\Resources\AgentJobResource\Pages\ViewAgentJob;
use App\Livewire\JobLog;
use App\Models\AgentJob;
use App\Models\Site;
use BackedEnum;
use Filament\Infolists\Components\TextEntry;
use Filament\Resources\Resource;
use Filament\Schemas\Components\Livewire;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Schema;
use Filament\Support\Icons\Heroicon;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Filters\SelectFilter;
use Filament\Tables\Table;

/** Every job the panel submitted: the list with filters, and each job's page with its live log (§7.3). */
class AgentJobResource extends Resource
{
    protected static ?string $model = AgentJob::class;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedQueueList;

    protected static ?int $navigationSort = 3;

    public static function getModelLabel(): string
    {
        return __('zimam.jobs.label');
    }

    public static function getPluralModelLabel(): string
    {
        return __('zimam.jobs.title');
    }

    public static function canCreate(): bool
    {
        return false;
    }

    public static function table(Table $table): Table
    {
        return $table
            ->defaultSort('id', 'desc')
            ->poll('5s')
            ->emptyStateHeading(__('zimam.jobs.empty'))
            ->recordUrl(fn (AgentJob $record): string => self::getUrl('view', ['record' => $record]))
            ->columns([
                TextColumn::make('command')
                    ->label(__('zimam.jobs.fields.command'))
                    ->fontFamily('mono'),
                TextColumn::make('subject')
                    ->label(__('zimam.jobs.fields.subject'))
                    ->placeholder('—'),
                TextColumn::make('status')
                    ->label(__('zimam.jobs.fields.status'))
                    ->badge()
                    ->formatStateUsing(fn (string $state): string => __("zimam.jobs.status.{$state}"))
                    ->color(fn (string $state): string => self::statusColor($state)),
                TextColumn::make('created_at')
                    ->label(__('zimam.jobs.fields.created_at'))
                    ->since()
                    ->dateTimeTooltip(),
                TextColumn::make('finished_at')
                    ->label(__('zimam.jobs.fields.finished_at'))
                    ->since()
                    ->dateTimeTooltip()
                    ->placeholder('—'),
            ])
            ->filters([
                SelectFilter::make('status')
                    ->label(__('zimam.jobs.fields.status'))
                    ->options(collect([AgentJob::WAITING, AgentJob::QUEUED, AgentJob::RUNNING, ...AgentJob::FINISHED])
                        ->mapWithKeys(fn (string $status): array => [$status => __("zimam.jobs.status.{$status}")])
                        ->all()),
                SelectFilter::make('command')
                    ->label(__('zimam.jobs.fields.command'))
                    ->options(fn (): array => AgentJob::query()->distinct()->orderBy('command')->pluck('command', 'command')->all()),
            ]);
    }

    public static function infolist(Schema $schema): Schema
    {
        return $schema->columns(1)->components([
            Section::make()->columns(3)->schema([
                TextEntry::make('command')
                    ->label(__('zimam.jobs.fields.command'))
                    ->fontFamily('mono'),
                TextEntry::make('subject')
                    ->label(__('zimam.jobs.fields.subject'))
                    ->placeholder('—'),
                TextEntry::make('status')
                    ->label(__('zimam.jobs.fields.status'))
                    ->badge()
                    ->formatStateUsing(fn (string $state): string => __("zimam.jobs.status.{$state}"))
                    ->color(fn (string $state): string => self::statusColor($state)),
                TextEntry::make('related')
                    ->label(__('zimam.jobs.fields.related'))
                    ->state(fn (AgentJob $record): ?string => $record->related instanceof Site ? $record->related->name : null)
                    ->url(fn (AgentJob $record): ?string => $record->related instanceof Site ? SiteResource::getUrl('view', ['record' => $record->related]) : null)
                    ->placeholder('—'),
                TextEntry::make('created_at')->label(__('zimam.jobs.fields.created_at'))->dateTime(),
                TextEntry::make('started_at')->label(__('zimam.jobs.fields.started_at'))->dateTime()->placeholder('—'),
                TextEntry::make('finished_at')->label(__('zimam.jobs.fields.finished_at'))->dateTime()->placeholder('—'),
                TextEntry::make('agent_job_id')->label(__('zimam.jobs.fields.agent_job_id'))->fontFamily('mono')->placeholder('—')->copyable(),
                TextEntry::make('error_message')
                    ->label(__('zimam.jobs.fields.error'))
                    ->visible(fn (AgentJob $record): bool => filled($record->error_message))
                    ->color('danger')
                    ->columnSpanFull(),
            ]),
            Section::make(__('zimam.jobs.log.heading'))->schema([
                Livewire::make(JobLog::class, fn (AgentJob $record): array => ['agentJob' => $record])->key('job-log'),
            ]),
        ]);
    }

    public static function statusColor(string $status): string
    {
        return match ($status) {
            AgentJob::SUCCEEDED => 'success',
            AgentJob::FAILED => 'danger',
            AgentJob::CANCELLED => 'gray',
            default => 'warning',
        };
    }

    public static function getPages(): array
    {
        return [
            'index' => ListAgentJobs::route('/'),
            'view' => ViewAgentJob::route('/{record}'),
        ];
    }
}
