<?php

namespace App\Providers;

use App\Settings\PanelSettings;
use App\Support\DisplayTime;
use Filament\Support\Facades\FilamentTimezone;
use Illuminate\Support\ServiceProvider;

class AppServiceProvider extends ServiceProvider
{
    /**
     * Register any application services.
     */
    public function register(): void
    {
        // One instance per request, so the display zone is read once.
        $this->app->scoped(PanelSettings::class);
    }

    /**
     * Bootstrap any application services.
     */
    public function boot(): void
    {
        // Filament's tables and entries show times in the panel's display zone.
        FilamentTimezone::set(fn (): string => DisplayTime::zone());
    }
}
