<?php

namespace App\Agent\Commands;

/** Sets the password of a site's MySQL user, making the user if it has none. */
final class DbUser extends ServiceUserCommand
{
    public function name(): string
    {
        return 'db.user';
    }
}
