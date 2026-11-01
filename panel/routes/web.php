<?php

use App\Livewire\Setup;
use Filament\Http\Middleware\DisableBladeIconComponents;
use Filament\Http\Middleware\DispatchServingFilamentEvent;
use Illuminate\Support\Facades\Route;

// The panel lives under /admin.
Route::redirect('/', '/admin');

// The one-time setup link (design §4), drawn like the panel's login page.
Route::get('/setup/{token}', Setup::class)
    ->middleware(['panel:admin', DisableBladeIconComponents::class, DispatchServingFilamentEvent::class])
    ->name('setup');
