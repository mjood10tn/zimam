<?php

namespace Tests\Feature\Access;

use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\Support\ConfirmsPasswordComponent;
use Tests\TestCase;

/** Deleting a site or uninstalling a PHP version asks for the password again (§7.2). */
final class ConfirmsPasswordTest extends TestCase
{
    use RefreshDatabase;

    protected function setUp(): void
    {
        parent::setUp();
        $this->actingAs(User::factory()->create(['password' => 'the right password']));
    }

    public function testWrongPasswordRejected(): void
    {
        Livewire::test(ConfirmsPasswordComponent::class)
            ->callAction('destroy', ['current_password' => 'not the password'])
            ->assertHasActionErrors(['current_password'])
            ->assertSet('destroyed', false);

        Livewire::test(ConfirmsPasswordComponent::class)
            ->callAction('destroy', ['current_password' => ''])
            ->assertHasActionErrors(['current_password'])
            ->assertSet('destroyed', false);
    }

    public function testRightPasswordRunsTheAction(): void
    {
        Livewire::test(ConfirmsPasswordComponent::class)
            ->callAction('destroy', ['current_password' => 'the right password'])
            ->assertHasNoErrors()
            ->assertSet('destroyed', true);
    }
}
