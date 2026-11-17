<?php

namespace App\Agent\Commands;

/**
 * Sets the password of a site's Valkey user, making the user if it has none.
 * On an existing user the agent then closes the user's open connections.
 */
final class CacheUser extends ServiceUserCommand
{
    public function name(): string
    {
        return 'cache.user';
    }
}
