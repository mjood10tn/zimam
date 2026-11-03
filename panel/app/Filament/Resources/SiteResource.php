<?php

namespace App\Filament\Resources;

use App\Filament\Resources\SiteResource\Pages\CreateSite;
use App\Filament\Resources\SiteResource\Pages\ListSites;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Livewire\SiteJobs;
use App\Models\Certificate;
use App\Models\Domain;
use App\Models\Site;
use BackedEnum;
use Closure;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\ToggleButtons;
use Filament\Infolists\Components\TextEntry;
use Filament\Resources\Resource;
use Filament\Schemas\Components\Livewire;
use Filament\Schemas\Components\Tabs;
use Filament\Schemas\Components\Tabs\Tab;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Schema;
use Filament\Support\Icons\Heroicon;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Table;

/** Sites: the list, the create form and each site's page with its tabs (§7.3, §9). */
class SiteResource extends Resource
{
    protected static ?string $model = Site::class;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedGlobeAlt;

    protected static ?int $navigationSort = 1;

    protected static ?string $recordTitleAttribute = 'name';

    public static function getModelLabel(): string
    {
        return __('zimam.sites.label');
    }

    public static function getPluralModelLabel(): string
    {
        return __('zimam.sites.title');
    }

    public static function form(Schema $schema): Schema
    {
        return $schema->columns(1)->components([
            TextInput::make('name')
                ->label(__('zimam.sites.fields.name'))
                ->helperText(__('zimam.sites.fields.name_help'))
                ->required()
                ->maxLength(24)
                ->regex(Site::NAME_PATTERN)
                ->notIn(Site::RESERVED_NAMES)
                ->unique(Site::class, 'name')
                ->validationMessages([
                    'regex' => __('zimam.sites.validation.name'),
                    'not_in' => __('zimam.sites.validation.reserved'),
                    'unique' => __('zimam.sites.validation.name_taken'),
                ]),
            TextInput::make('domain')
                ->label(__('zimam.sites.fields.domain'))
                ->helperText(__('zimam.sites.fields.domain_help'))
                ->required()
                ->maxLength(253)
                ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                    $hostname = Domain::normalize((string) $value);
                    if (! Domain::isValid($hostname)) {
                        $fail(__('zimam.sites.validation.domain'));
                    } elseif (Domain::where('hostname', $hostname)->exists()) {
                        $fail(__('zimam.sites.validation.domain_taken'));
                    }
                })
                ->dehydrateStateUsing(fn (?string $state): string => Domain::normalize((string) $state)),
            ToggleButtons::make('type')
                ->label(__('zimam.sites.fields.type'))
                ->options([Site::PHP => __('zimam.sites.types.php'), Site::STATIC => __('zimam.sites.types.static')])
                ->default(Site::PHP)
                ->inline()
                ->live()
                ->required()
                ->in([Site::PHP, Site::STATIC]),
            Select::make('php_version')
                ->label(__('zimam.sites.fields.php_version'))
                ->options(fn (CreateSite $livewire): array => $livewire->phpVersionOptions())
                ->default(fn (CreateSite $livewire): ?string => $livewire->defaultPhpVersion())
                ->noOptionsMessage(__('zimam.sites.fields.php_none'))
                ->visible(fn (Get $get): bool => $get('type') === Site::PHP)
                ->required(fn (Get $get): bool => $get('type') === Site::PHP)
                ->selectablePlaceholder(false),
        ]);
    }

    public static function infolist(Schema $schema): Schema
    {
        return $schema->columns(1)->components([
            Tabs::make()->tabs([
                Tab::make(__('zimam.sites.tabs.overview'))->columns(2)->schema([
                    TextEntry::make('status')
                        ->label(__('zimam.sites.fields.status'))
                        ->badge()
                        ->formatStateUsing(fn (string $state): string => __("zimam.sites.status.{$state}"))
                        ->color(fn (string $state): string => self::statusColor($state)),
                    TextEntry::make('url')
                        ->label(__('zimam.sites.fields.url'))
                        ->state(fn (Site $record): string => $record->url())
                        ->url(fn (Site $record): string => $record->url())
                        ->openUrlInNewTab(),
                    TextEntry::make('type')
                        ->label(__('zimam.sites.fields.type'))
                        ->formatStateUsing(fn (string $state): string => __("zimam.sites.types.{$state}")),
                    TextEntry::make('php_version')
                        ->label(__('zimam.sites.fields.php_version'))
                        ->placeholder(__('zimam.sites.fields.php_static')),
                    TextEntry::make('certificate')
                        ->label(__('zimam.sites.fields.certificate'))
                        ->state(fn (Site $record): string => match ($record->certificate?->status) {
                            Certificate::ISSUED => __('zimam.sites.certificate.issued', ['date' => $record->certificate->expires_at?->setTimezone(config('app.timezone'))->format('j M Y')]),
                            Certificate::ISSUING => __('zimam.sites.certificate.issuing'),
                            Certificate::FAILED => __('zimam.sites.certificate.failed'),
                            default => __('zimam.sites.certificate.none'),
                        }),
                    TextEntry::make('certificate.last_error')
                        ->label(__('zimam.sites.fields.certificate_error'))
                        ->visible(fn (Site $record): bool => $record->certificate?->status === Certificate::FAILED && filled($record->certificate->last_error))
                        ->columnSpanFull(),
                    TextEntry::make('template_version')
                        ->label(__('zimam.sites.fields.template_version'))
                        ->placeholder('—'),
                    TextEntry::make('last_error')
                        ->label(__('zimam.sites.fields.last_error'))
                        ->visible(fn (Site $record): bool => filled($record->last_error))
                        ->columnSpanFull(),
                ]),
                Tab::make(__('zimam.sites.tabs.jobs'))->schema([
                    Livewire::make(SiteJobs::class, fn (Site $record): array => ['site' => $record])->key('site-jobs'),
                ]),
                Tab::make(__('zimam.sites.tabs.security'))->schema([Text::make(__('zimam.sites.tabs.later'))]),
                Tab::make(__('zimam.sites.tabs.logs'))->schema([Text::make(__('zimam.sites.tabs.later'))]),
            ]),
        ]);
    }

    public static function table(Table $table): Table
    {
        return $table
            ->defaultSort('name')
            ->emptyStateHeading(__('zimam.sites.empty'))
            ->columns([
                TextColumn::make('name')
                    ->label(__('zimam.sites.fields.name'))
                    ->weight('bold')
                    ->searchable(),
                TextColumn::make('primaryDomain.hostname')
                    ->label(__('zimam.sites.fields.domain')),
                TextColumn::make('type')
                    ->label(__('zimam.sites.fields.type'))
                    ->formatStateUsing(fn (string $state): string => __("zimam.sites.types.{$state}")),
                TextColumn::make('php_version')
                    ->label(__('zimam.sites.fields.php_version'))
                    ->placeholder('—'),
                TextColumn::make('status')
                    ->label(__('zimam.sites.fields.status'))
                    ->badge()
                    ->formatStateUsing(fn (string $state): string => __("zimam.sites.status.{$state}"))
                    ->color(fn (string $state): string => self::statusColor($state)),
            ]);
    }

    public static function statusColor(string $status): string
    {
        return match ($status) {
            Site::ACTIVE => 'success',
            Site::FAILED => 'danger',
            default => 'warning',
        };
    }

    public static function getPages(): array
    {
        return [
            'index' => ListSites::route('/'),
            'create' => CreateSite::route('/create'),
            'view' => ViewSite::route('/{record}'),
        ];
    }
}
