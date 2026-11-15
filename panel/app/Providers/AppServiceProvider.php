<?php

namespace App\Providers;

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
        //
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
