<?php

namespace App\Filament\Pages\Auth;

use Filament\Auth\Pages\Login as BaseLogin;
use Illuminate\Support\Str;

/** Filament's login, throttled per email and IP address (§7.2) rather than per IP alone. */
class Login extends BaseLogin
{
    /**
     * rateLimit(5) runs before the form is validated, so this reads the raw
     * form state. The email is trimmed and lowercased, so changing its case
     * or padding it doesn't buy five fresh attempts.
     */
    protected function getRateLimitKey($method, $component = null)
    {
        $method ??= debug_backtrace(DEBUG_BACKTRACE_IGNORE_ARGS, limit: 2)[1]['function'];
        $component ??= static::class;
        $email = Str::lower(trim((string) ($this->data['email'] ?? '')));

        return 'livewire-rate-limiter:'.sha1($component.'|'.$method.'|'.$email.'|'.request()->ip());
    }
}
