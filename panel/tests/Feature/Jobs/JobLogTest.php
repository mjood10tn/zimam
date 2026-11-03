<?php

namespace Tests\Feature\Jobs;

use App\Agent\FileJobLogReader;
use App\Agent\JobLogReader;
use App\Livewire\JobLog;
use App\Models\AgentJob;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use InvalidArgumentException;
use Livewire\Livewire;
use Tests\TestCase;

/** The live job log: Livewire polls the job's log file from the last offset (§7.3). */
final class JobLogTest extends TestCase
{
    use RefreshDatabase;

    private const JOB = '01JBX7Q3A1B2C3D4E5F6G7H8J9';

    private string $dir;

    protected function setUp(): void
    {
        parent::setUp();
        $this->dir = sys_get_temp_dir().'/zimam-joblogs-'.bin2hex(random_bytes(4));
        mkdir($this->dir);
        $this->app->instance(JobLogReader::class, new FileJobLogReader($this->dir));
        $this->actingAs(User::factory()->create());
    }

    protected function tearDown(): void
    {
        array_map('unlink', glob($this->dir.'/*'));
        rmdir($this->dir);
        parent::tearDown();
    }

    private function append(string $text): void
    {
        file_put_contents($this->dir.'/'.self::JOB.'.log', $text, FILE_APPEND);
    }

    public function testLogReturnsOnlyNewLines(): void
    {
        $reader = new FileJobLogReader($this->dir);
        $this->assertSame([], $reader->read(self::JOB, 0)->lines, 'no log yet');

        $this->append("[2026-11-03T09:00:00Z] one\n[2026-11-03T09:00:01Z] two\n");
        $first = $reader->read(self::JOB, 0);
        $this->assertSame(['[2026-11-03T09:00:00Z] one', '[2026-11-03T09:00:01Z] two'], $first->lines);

        // A line still being written waits for its newline.
        $this->append('[2026-11-03T09:00:02Z] thr');
        $partial = $reader->read(self::JOB, $first->offset);
        $this->assertSame([], $partial->lines);
        $this->assertSame($first->offset, $partial->offset);

        $this->append("ee\n[2026-11-03T09:00:03Z] four\n");
        $this->assertSame(['[2026-11-03T09:00:02Z] three', '[2026-11-03T09:00:03Z] four'], $reader->read(self::JOB, $partial->offset)->lines);
    }

    public function testComponentPollsFromTheLastOffset(): void
    {
        $job = AgentJob::create(['agent_job_id' => self::JOB, 'command' => 'site.provision', 'status' => AgentJob::RUNNING, 'params' => []]);
        $this->append("[2026-11-03T09:00:00Z] Creating the user s_shop\n");

        $log = Livewire::test(JobLog::class, ['agentJob' => $job])
            ->assertSee('Creating the user s_shop')
            ->assertSeeHtml('wire:poll.2s');

        $this->append("[2026-11-03T09:00:01Z] Reloading Nginx\n");
        $log->call('poll')->assertSee('Reloading Nginx');
        $this->assertSame(['[2026-11-03T09:00:00Z] Creating the user s_shop', '[2026-11-03T09:00:01Z] Reloading Nginx'], $log->get('lines'));

        $log->call('poll');
        $this->assertCount(2, $log->get('lines'), 'a poll with nothing new adds nothing');

        $job->update(['status' => AgentJob::SUCCEEDED]);
        $log->call('poll')->assertDontSeeHtml('wire:poll.2s');
    }

    /** The path is built only from a job id that passed the ULID check (§5.4). */
    public function testReaderRejectsInvalidId(): void
    {
        $reader = new FileJobLogReader($this->dir);
        $ids = ['../../etc/passwd', '01JBX7Q3A1B2C3D4E5F6G7H8J', self::JOB."\n", strtolower(self::JOB), '01JBX7Q3A1B2C3D4E5F6G7H8JU'];
        foreach ($ids as $id) {
            try {
                $reader->read($id, 0);
                $this->fail("read accepted the job id {$id}");
            } catch (InvalidArgumentException) {
                $this->addToAssertionCount(1);
            }
        }
    }
}
