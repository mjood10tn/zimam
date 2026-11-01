<?php

namespace Tests\Support;

use App\Agent\AgentHealth;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Livewire\Component;

/** A page with one action that calls the agent, as every later screen has. */
final class GuardedActionComponent extends Component implements HasActions, HasSchemas
{
    use InteractsWithActions;
    use InteractsWithSchemas;

    public bool $renewed = false;

    public function renewAction(): Action
    {
        return AgentHealth::guard(
            Action::make('renew')->action(function (): void {
                $this->renewed = true;
            }),
        );
    }

    public function render(): string
    {
        return <<<'BLADE'
            <div>
                {{ $this->renewAction }}
                <x-filament-actions::modals />
            </div>
            BLADE;
    }
}
