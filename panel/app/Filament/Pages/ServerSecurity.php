<?php

namespace App\Filament\Pages;

use App\Filament\Pages\ServerSecurity\CloudflareForm;
use App\Filament\Pages\ServerSecurity\Fail2banForm;
use App\Filament\Pages\ServerSecurity\FirewallTable;
use App\Filament\Pages\ServerSecurity\SshForm;
use App\Livewire\GuardedChangeModal;
use BackedEnum;
use Filament\Pages\Page;
use Filament\Schemas\Components\Livewire;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Schema;
use Filament\Support\Icons\Heroicon;

/**
 * Server security (v0.6.0): the firewall rules and the SSH settings, each a
 * guarded change the owner keeps from the countdown modal (R06-D1, D2),
 * then fail2ban and Cloudflare mode (R06-D5, D7, D9).
 */
class ServerSecurity extends Page
{
    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedLockClosed;

    protected static ?int $navigationSort = 2;

    public static function getNavigationLabel(): string
    {
        return __('zimam.server_security.title');
    }

    public function getTitle(): string
    {
        return __('zimam.server_security.title');
    }

    public function content(Schema $schema): Schema
    {
        return $schema->components([
            Text::make(__('zimam.server_security.lede')),
            Livewire::make(GuardedChangeModal::class)->key('guarded-change'),
            Livewire::make(FirewallTable::class)->key('firewall-rules'),
            Livewire::make(SshForm::class)->key('ssh-settings'),
            Livewire::make(Fail2banForm::class)->key('fail2ban'),
            Livewire::make(CloudflareForm::class)->key('cloudflare'),
        ]);
    }
}
