<?php

namespace Tests\Support;

use App\Filament\Actions\ConfirmsPassword;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Livewire\Component;

/** A page with one destructive action behind ConfirmsPassword, as Tasks 25 and 29 will have. */
final class ConfirmsPasswordComponent extends Component implements HasActions, HasSchemas
{
    use InteractsWithActions;
    use InteractsWithSchemas;

    public bool $destroyed = false;

    public function destroyAction(): Action
    {
        return ConfirmsPassword::apply(
            Action::make('destroy')->action(function (): void {
                $this->destroyed = true;
            }),
        );
    }

    public function render(): string
    {
        return <<<'BLADE'
            <div>
                {{ $this->destroyAction }}
                <x-filament-actions::modals />
            </div>
            BLADE;
    }
}
