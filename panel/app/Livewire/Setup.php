<?php

namespace App\Livewire;

use App\Models\SetupToken;
use App\Models\User;
use Filament\Actions\Action;
use Filament\Facades\Filament;
use Filament\Forms\Components\TextInput;
use Filament\Pages\SimplePage;
use Filament\Schemas\Components\Actions;
use Filament\Schemas\Components\EmbeddedSchema;
use Filament\Schemas\Components\Form;
use Filament\Schemas\Schema;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Illuminate\Validation\Rules\Password;
use Illuminate\Validation\ValidationException;
use Livewire\Attributes\Locked;

/**
 * The page behind a one-time setup link (design §4). With no admin it
 * creates one; with one (the recovery path) it resets that admin's email,
 * password and 2FA and signs out its sessions. Either way the admin is then
 * logged in, and Filament asks for 2FA before anything else.
 */
class Setup extends SimplePage
{
    #[Locked]
    public string $token = '';

    /** Whether the page opened with an admin already there. */
    #[Locked]
    public bool $isReset = false;

    /** @var array<string, mixed>|null */
    public ?array $data = [];

    public function mount(string $token): void
    {
        abort_unless(SetupToken::findUsable($token), 404);

        $this->token = $token;
        $this->isReset = User::query()->exists();
        $this->form->fill();
    }

    public function form(Schema $schema): Schema
    {
        return $schema
            ->components([
                TextInput::make('email')
                    ->label(__('zimam.setup.email'))
                    ->email()
                    ->required()
                    ->maxLength(255)
                    ->autocomplete('username')
                    ->autofocus(),
                TextInput::make('password')
                    ->label(__('zimam.setup.password'))
                    ->helperText(__('zimam.setup.password_help'))
                    ->password()
                    ->revealable()
                    ->required()
                    ->rule(Password::min(12))
                    ->confirmed()
                    ->autocomplete('new-password'),
                TextInput::make('password_confirmation')
                    ->label(__('zimam.setup.password_confirmation'))
                    ->password()
                    ->revealable()
                    ->required()
                    ->autocomplete('new-password'),
            ])
            ->statePath('data');
    }

    public function content(Schema $schema): Schema
    {
        return $schema
            ->components([
                Form::make([EmbeddedSchema::make('form')])
                    ->id('form')
                    ->livewireSubmitHandler('save')
                    ->footer([
                        Actions::make([
                            Action::make('save')
                                ->label(__($this->isReset ? 'zimam.setup.reset.submit' : 'zimam.setup.create.submit'))
                                ->submit('save'),
                        ])->fullWidth(),
                    ]),
            ]);
    }

    public function save(): void
    {
        $data = $this->form->getState();
        // Stored in lowercase, as sign-in compares it, so the case it was typed in never matters.
        $data['email'] = Str::lower(trim($data['email']));
        $token = SetupToken::findUsable($this->token);
        abort_unless($token, 404);

        $user = DB::transaction(function () use ($data, $token): User {
            $user = User::query()->first();
            if ($user === null) {
                $user = User::query()->create([
                    'name' => __('zimam.setup.admin_name'),
                    'email' => $data['email'],
                    'password' => $data['password'],
                ]);
            } elseif (! $this->isReset) {
                // Opened before the admin existed and saved after: one admin only (§7.2).
                throw ValidationException::withMessages(['data.email' => __('zimam.setup.admin_exists')]);
            } else {
                $user->forceFill([
                    'email' => $data['email'],
                    'password' => $data['password'],
                    'app_authentication_secret' => null,
                    'app_authentication_recovery_codes' => null,
                    // Ends logins kept by "Remember me" (Task 3's owner decision).
                    'remember_token' => Str::random(60),
                ])->save();
                DB::table('sessions')->where('user_id', $user->getKey())->delete();
            }
            $token->markUsed();

            return $user;
        });

        Filament::auth()->login($user);
        session()->regenerate();

        $this->redirect(Filament::getPanel('admin')->getUrl());
    }

    public function getTitle(): string|Htmlable
    {
        return __($this->isReset ? 'zimam.setup.reset.title' : 'zimam.setup.create.title');
    }

    public function getHeading(): string|Htmlable
    {
        return __($this->isReset ? 'zimam.setup.reset.heading' : 'zimam.setup.create.heading');
    }

    public function getSubheading(): string|Htmlable|null
    {
        return __($this->isReset ? 'zimam.setup.reset.subheading' : 'zimam.setup.create.subheading');
    }
}
