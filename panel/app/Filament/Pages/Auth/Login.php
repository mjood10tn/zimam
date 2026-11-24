<?php

namespace App\Filament\Pages\Auth;

use App\Support\AuthFailureLog;
use Filament\Auth\Http\Responses\Contracts\LoginResponse;
use Filament\Auth\Pages\Login as BaseLogin;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;
use SensitiveParameter;

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

    /**
     * A wrong 2FA code fires no auth event, so it is logged here for
     * fail2ban's zimam-panel jail (R06-D6). Only errors on the challenge's
     * own fields count: a password that fails its second check fires Failed,
     * which is logged already.
     */
    public function authenticate(): ?LoginResponse
    {
        try {
            return parent::authenticate();
        } catch (ValidationException $e) {
            if (collect($e->errors())->keys()->contains(fn (string $key): bool => str_starts_with($key, 'data.multiFactor.'))) {
                app(AuthFailureLog::class)->record('totp', request()->ip());
            }
            throw $e;
        }
    }

    /** The admin's email is stored in lowercase (Setup), so it's compared that way. */
    protected function getCredentialsFromFormData(#[SensitiveParameter] array $data): array
    {
        return [
            'email' => Str::lower(trim((string) $data['email'])),
            'password' => $data['password'],
        ];
    }
}
