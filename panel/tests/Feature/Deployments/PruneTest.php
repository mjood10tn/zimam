<?php

namespace Tests\Feature\Deployments;

use App\Models\AgentJob;
use App\Models\Deployment;
use App\Models\SiteRepository;
use App\Models\WebhookDelivery;
use Illuminate\Support\Facades\Artisan;

/** zimam:prune-history drops webhook deliveries after 24 hours (R02-D11), and deployments with their jobs. */
final class PruneTest extends DeploymentsTestCase
{
    public function testDeliveriesKeptForADay(): void
    {
        $this->freezeSecond();
        $site = $this->site();
        $old = WebhookDelivery::create(['delivery_id' => 'old', 'site_id' => $site->id, 'received_at' => now()->subHours(24)->subSecond()]);
        $recent = WebhookDelivery::create(['delivery_id' => 'recent', 'site_id' => $site->id, 'received_at' => now()->subHours(23)]);

        $this->assertSame(0, Artisan::call('zimam:prune-history'));

        $this->assertModelMissing($old);
        $this->assertModelExists($recent);
    }

    /** A deployment is its job's record: it goes when the job is pruned, and a recent one stays. */
    public function testDeploymentsGoWithTheirJobs(): void
    {
        $this->freezeSecond();
        $site = $this->site();
        $deployment = function (int $days) use ($site): Deployment {
            $job = AgentJob::create(['command' => 'site.deploy', 'status' => AgentJob::SUCCEEDED, 'params' => []]);
            $job->forceFill(['created_at' => now()->subDays($days)])->save();

            return Deployment::create(['site_id' => $site->id, 'agent_job_id' => $job->id, 'trigger' => Deployment::MANUAL, 'status' => Deployment::SUCCEEDED]);
        };
        $old = $deployment(15);
        $recent = $deployment(2);

        $this->assertSame(0, Artisan::call('zimam:prune-history'));

        $this->assertModelMissing($old);
        $this->assertModelExists($recent);
    }

    public function testSiteDeletionTakesItsRows(): void
    {
        $site = $this->site();
        $this->connected($site);
        WebhookDelivery::create(['delivery_id' => 'd1', 'site_id' => $site->id, 'received_at' => now()]);
        $job = AgentJob::create(['command' => 'site.deploy', 'status' => AgentJob::SUCCEEDED, 'params' => []]);
        Deployment::create(['site_id' => $site->id, 'agent_job_id' => $job->id, 'trigger' => Deployment::MANUAL, 'status' => Deployment::SUCCEEDED]);

        $site->delete();

        $this->assertSame(0, SiteRepository::count());
        $this->assertSame(0, WebhookDelivery::count());
        $this->assertSame(0, Deployment::count());
    }
}
