<?php

namespace App\Filament\Actions;

use Closure;
use Filament\Actions\Action;
use Filament\Forms\Components\TextInput;
use Illuminate\Support\Facades\Hash;
use SensitiveParameter;

/**
 * Makes an action ask for the current password before it runs (§7.2), for
 * deleting a site or uninstalling a PHP version. It sets the action's form.
 */
final class ConfirmsPassword
{
    public static function apply(Action $action): Action
    {
        return $action->schema([
            TextInput::make('current_password')
                ->label(__('zimam.confirm_password.label'))
                ->password()
                ->revealable()
                ->autocomplete('current-password')
                ->required()
                ->rule(fn (): Closure => function (string $attribute, #[SensitiveParameter] mixed $value, Closure $fail): void {
                    $password = auth()->user()?->getAuthPassword();
                    if (! is_string($value) || ! is_string($password) || ! Hash::check($value, $password)) {
                        $fail(__('zimam.confirm_password.wrong'));
                    }
                }),
        ]);
    }
}
