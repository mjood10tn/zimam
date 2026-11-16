<?php

namespace Tests\Feature\Hooks;

use App\Models\Deployment;
use App\Models\SiteRepository;

/** POST /hooks/deploy/{site}/{token}: the site's deploy URL deploys its branch (R02-D11). */
final class DeployUrlTest extends HooksTestCase
{
    public function testDeployUrlWithGoodToken(): void
    {
        $site = $this->site();
        $repository = $this->connected($site);
        $token = $repository->regenerateDeployToken();

        // The deploy URL doesn't wait for auto-deploy: calling it is the request.
        $this->assertFalse($repository->auto_deploy);
        $this->postRaw(parse_url($repository->deployUrl($token), PHP_URL_PATH), '')->assertStatus(202);

        $deploys = $this->deploys();
        $this->assertCount(1, $deploys);
        $this->assertSame('url', $deploys[0]['trigger']);
        $this->assertArrayNotHasKey('commit', $deploys[0]);
        $deployment = Deployment::sole();
        $this->assertSame('url', $deployment->trigger);
        $this->assertNull($deployment->created_by);
    }

    public function testDeployUrlWithBadTokenIs404(): void
    {
        $repository = $this->connected($this->site());
        $old = $repository->revealedDeployToken;
        $repository->regenerateDeployToken();

        $responses = [
            $this->postRaw("/hooks/deploy/shop/{$old}", ''),
            $this->postRaw('/hooks/deploy/shop/'.str_repeat('0', 64), ''),
            $this->postRaw('/hooks/deploy/shop/x', ''),
            $this->postRaw('/hooks/deploy/shop/'.strtoupper((string) $old), ''),
        ];

        foreach ($responses as $response) {
            $response->assertNotFound();
            $this->assertSame($this->unknownSiteBody(), $response->getContent());
        }
        $this->assertNoJob();
    }

    public function testUnknownSiteIs404(): void
    {
        $repository = $this->connected($this->site());
        $token = $repository->regenerateDeployToken();
        $this->site(name: 'blog');

        $unknown = $this->postRaw("/hooks/deploy/nosuchsite/{$token}", '');
        $noRepository = $this->postRaw("/hooks/deploy/blog/{$token}", '');

        $unknown->assertNotFound();
        $noRepository->assertNotFound();
        $this->assertSame($unknown->getContent(), $noRepository->getContent());
        $this->assertNoJob();
    }

    public function testNotConnectedYet(): void
    {
        $repository = SiteRepository::createFor($this->site(), 'octo/shop', 'main');

        $this->postRaw("/hooks/deploy/shop/{$repository->revealedDeployToken}", '')->assertStatus(409);

        $this->assertNoJob();
    }

    public function testOnlyPost(): void
    {
        $repository = $this->connected($this->site());
        $token = $repository->regenerateDeployToken();

        $this->get("/hooks/deploy/shop/{$token}")->assertStatus(405);

        $this->assertNoJob();
    }

    private function unknownSiteBody(): string
    {
        return (string) $this->postRaw('/hooks/deploy/nosuchsite/'.str_repeat('0', 64), '')->getContent();
    }
}
