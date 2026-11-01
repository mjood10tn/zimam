<?php

namespace App\Providers\Filament;

use App\Agent\AgentHealth;
use App\Filament\Auth\LongSecretAppAuthentication;
use App\Filament\Pages\Auth\Login;
use Filament\Auth\MultiFactor\Http\Middleware\EnsureMultiFactorAuthenticationIsEnabled;
use Filament\Http\Middleware\Authenticate;
use Filament\Http\Middleware\AuthenticateSession;
use Filament\Http\Middleware\DisableBladeIconComponents;
use Filament\Http\Middleware\DispatchServingFilamentEvent;
use Filament\Panel;
use Filament\PanelProvider;
use Filament\Support\Colors\Color;
use Filament\View\PanelsRenderHook;
use Illuminate\Cookie\Middleware\AddQueuedCookiesToResponse;
use Illuminate\Cookie\Middleware\EncryptCookies;
use Illuminate\Foundation\Http\Middleware\PreventRequestForgery;
use Illuminate\Routing\Middleware\SubstituteBindings;
use Illuminate\Session\Middleware\StartSession;
use Illuminate\View\Middleware\ShareErrorsFromSession;

class AdminPanelProvider extends PanelProvider
{
    public function panel(Panel $panel): Panel
    {
        return $panel
            ->default()
            ->id('admin')
            ->path('admin')
            ->login(Login::class)
            ->brandName(fn (): string => __('zimam.brand'))
            // Required 2FA with authenticator apps and recovery codes (§7.2).
            ->multiFactorAuthentication([
                LongSecretAppAuthentication::make()->recoverable(),
            ], isRequired: true)
            ->colors([
                'primary' => Color::Amber,
            ])
            ->discoverResources(in: app_path('Filament/Resources'), for: 'App\Filament\Resources')
            // App\Filament\Pages\Dashboard is found here and replaces Filament's.
            ->discoverPages(in: app_path('Filament/Pages'), for: 'App\Filament\Pages')
            ->discoverWidgets(in: app_path('Filament/Widgets'), for: 'App\Filament\Widgets')
            // The blocking banner while the agent doesn't answer (D10).
            ->renderHook(PanelsRenderHook::CONTENT_START, fn (): string => app(AgentHealth::class)->isReachable()
                ? ''
                : view('filament.agent-banner', ['since' => app(AgentHealth::class)->lastAnswer()])->render())
            ->middleware([
                EncryptCookies::class,
                AddQueuedCookiesToResponse::class,
                StartSession::class,
                AuthenticateSession::class,
                ShareErrorsFromSession::class,
                PreventRequestForgery::class,
                SubstituteBindings::class,
                DisableBladeIconComponents::class,
                DispatchServingFilamentEvent::class,
            ])
            ->authMiddleware([
                Authenticate::class,
            ])
            // Livewire update requests re-run only persistent middleware; without
            // this one they skip the 2FA check (Task 3, step 3).
            ->persistentMiddleware([
                EnsureMultiFactorAuthenticationIsEnabled::class,
            ]);
    }
}
