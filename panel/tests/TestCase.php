<?php

namespace Tests;

use Illuminate\Foundation\Testing\TestCase as BaseTestCase;

abstract class TestCase extends BaseTestCase
{
    /** CI runs the tests before npm run build, so no page may need the Vite manifest. */
    protected function setUp(): void
    {
        parent::setUp();
        $this->withoutVite();
    }
}
