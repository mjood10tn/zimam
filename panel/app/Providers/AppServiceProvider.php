<?php

namespace App\Providers;

use App\Settings\PanelSettings;
use App\Support\AuthFailureLog;
use App\Support\DisplayTime;
use Filament\Support\Facades\FilamentTimezone;
use Illuminate\Auth\Events\Failed;
use Illuminate\Support\Facades\Event;
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
        // A failed password is logged for fail2ban's zimam-panel jail (R06-D6).
        Event::listen(Failed::class, fn () => app(AuthFailureLog::class)->record('password', request()->ip()));
        // Filament's tables and entries show times in the panel's display zone.
        FilamentTimezone::set(fn (): string => DisplayTime::zone());
    }
}
