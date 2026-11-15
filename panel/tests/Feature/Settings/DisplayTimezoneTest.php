<?php

namespace Tests\Feature\Settings;

use App\Agent\AgentClient;
use App\Agent\FakeAgentClient;
use App\Filament\Pages\Settings;
use App\Filament\Resources\AgentJobResource\Pages\ViewAgentJob;
use App\Models\AgentJob;
use App\Livewire\LogViewer;
use App\Models\Setting;
use App\Models\User;
use App\Settings\PanelSettings;
use App\Support\DisplayTime;
use Carbon\CarbonImmutable;
use Filament\Forms\Components\Select;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Queue;
use Livewire\Livewire;
use Tests\TestCase;

/** The panel's display time zone (R01-D19): panel only, storage stays UTC. */
final class DisplayTimezoneTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        Queue::fake();
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $user = User::factory()->create();
        $user->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
        $this->actingAs($user);
    }

    public function testDefaultIsUtc(): void
    {
        $this->assertSame('UTC', app(PanelSettings::class)->displayTimezone());
        $this->assertSame('UTC', DisplayTime::zone());
        $this->assertSame('UTC', config('app.timezone'));
    }

    public function testDropdownListsZonesAndPreselectsSetting(): void
    {
        app(PanelSettings::class)->setDisplayTimezone('Asia/Riyadh');

        Livewire::test(Settings::class)
            ->assertFormSet(['display_timezone' => 'Asia/Riyadh'])
            ->assertFormFieldExists('display_timezone', function (Select $field): bool {
                $options = $field->getOptions();

                return $field->isSearchable()
                    && $field->getLabel() === __('zimam.settings.display_timezone')
                    && ($options['Asia']['Asia/Riyadh'] ?? null) === 'Asia/Riyadh'
                    && ($options['Europe']['Europe/London'] ?? null) === 'Europe/London';
            });
    }

    public function testSaveStoresZone(): void
    {
        Livewire::test(Settings::class)
            ->fillForm(['display_timezone' => 'Asia/Riyadh'])
            ->call('save')
            ->assertHasNoFormErrors()
            ->assertNotified(__('zimam.settings.timezone_saved'));

        $this->assertSame('Asia/Riyadh', app(PanelSettings::class)->displayTimezone());
    }

    public function testUnknownZoneRefused(): void
    {
        foreach (['Mars/Base', '../etc', '', 'asia/riyadh'] as $zone) {
            Livewire::test(Settings::class)
                ->fillForm(['display_timezone' => $zone])
                ->call('save')
                ->assertHasFormErrors(['display_timezone']);
            $this->assertSame('UTC', app(PanelSettings::class)->displayTimezone());

            try {
                app(PanelSettings::class)->setDisplayTimezone($zone);
                $this->fail("'{$zone}' was accepted");
            } catch (\InvalidArgumentException) {
                $this->assertSame('UTC', app(PanelSettings::class)->displayTimezone());
            }
        }
    }

    public function testTimesShownInDisplayZone(): void
    {
        $job = AgentJob::create(['command' => 'logs.policy', 'subject' => 'server', 'status' => AgentJob::SUCCEEDED, 'params' => [], 'finished_at' => CarbonImmutable::parse('2026-11-11 09:00:00', 'UTC')]);

        Livewire::test(ViewAgentJob::class, ['record' => $job->getKey()])->assertSee('09:00:00')->assertDontSee('12:00:00');

        app(PanelSettings::class)->setDisplayTimezone('Asia/Riyadh');
        Livewire::test(ViewAgentJob::class, ['record' => $job->getKey()])->assertSee('12:00:00')->assertDontSee('09:00:00');

        $at = CarbonImmutable::parse('2026-11-11 09:00:00', 'UTC');
        $this->assertSame('12:00', DisplayTime::format($at, 'H:i'));
        $this->assertSame('09:00', $at->format('H:i'), 'the stored instant is untouched');
        $this->assertSame('', DisplayTime::format(null, 'H:i'));
    }

    public function testStorageStaysUtc(): void
    {
        app(PanelSettings::class)->setDisplayTimezone('Asia/Riyadh');
        Livewire::test(Settings::class)->fillForm(['display_timezone' => 'Asia/Riyadh'])->call('save');

        $this->travelTo(CarbonImmutable::parse('2026-11-11 09:00:00', 'UTC'));
        $job = AgentJob::create(['command' => 'logs.policy', 'subject' => 'server', 'status' => AgentJob::QUEUED, 'params' => []]);
        $job->update(['queued_at' => now()]);

        $this->assertSame('2026-11-11 09:00:00', \DB::table('agent_jobs')->where('id', $job->id)->value('created_at'));
        $this->assertSame('2026-11-11 09:00:00', \DB::table('agent_jobs')->where('id', $job->id)->value('queued_at'));
        $this->assertSame('UTC', config('app.timezone'));
    }

    public function testNoAgentCallOnSave(): void
    {
        Livewire::test(Settings::class)
            ->fillForm(['display_timezone' => 'Asia/Riyadh'])
            ->call('save')
            ->assertHasNoFormErrors();

        $this->assertSame('Asia/Riyadh', app(PanelSettings::class)->displayTimezone());
        $this->assertSame([], $this->agent->calls());
        $this->assertSame(0, AgentJob::count());
    }

    public function testBadStoredValueFallsBackToUtc(): void
    {
        Setting::query()->create(['key' => 'display_timezone', 'value' => 'Mars/Base']);

        $this->assertSame('UTC', app(PanelSettings::class)->displayTimezone());
        $this->assertSame('09:00', DisplayTime::format(CarbonImmutable::parse('2026-11-11 09:00:00', 'UTC'), 'H:i'));
    }

    public function testJournalLinesShownInDisplayZone(): void
    {
        $this->travelTo(CarbonImmutable::parse('2026-11-11 09:00:00', 'UTC'));
        $meta = fn (): string => Livewire::test(LogViewer::class, ['source' => 'journal.nginx'])->get('lines')[0]['meta'];

        $this->assertStringStartsWith('2026-11-11 09:00:00', $meta());
        app(PanelSettings::class)->setDisplayTimezone('Asia/Riyadh');
        $this->assertStringStartsWith('2026-11-11 12:00:00', $meta());
    }
}
