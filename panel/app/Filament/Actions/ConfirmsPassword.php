<?php

namespace App\Filament\Actions;

use Closure;
use Filament\Actions\Action;
use Filament\Forms\Components\TextInput;
use Filament\Schemas\Components\Component;
use Illuminate\Support\Facades\Hash;
use SensitiveParameter;

/**
 * Makes an action ask for the current password before it runs (§7.2), for
 * deleting a site or uninstalling a PHP version. It sets the action's form:
 * any $fields the action needs, then the password.
 */
final class ConfirmsPassword
{
    /** @param  list<Component>  $fields */
    public static function apply(Action $action, array $fields = []): Action
    {
        return $action->schema([
            ...$fields,
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
