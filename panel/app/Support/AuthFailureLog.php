<?php

namespace App\Support;

use Closure;

/**
 * One journal line per failed sign-in, for fail2ban's zimam-panel jail
 * (R06-D6): "auth failure from <address> kind=<password|totp|setup>", with
 * the ident zimam-panel-auth. The address is REMOTE_ADDR as Nginx passes it;
 * the email is never logged.
 */
final class AuthFailureLog
{
    public const IDENT = 'zimam-panel-auth';

    public const KINDS = ['password', 'totp', 'setup'];

    /** @param  (Closure(string): void)|null  $write  writes one line; the journal when null */
    public function __construct(private readonly ?Closure $write = null) {}

    public function record(string $kind, ?string $address): void
    {
        // Only a parsed address reaches the line, so nothing a client sends
        // can shape what fail2ban reads.
        if (! in_array($kind, self::KINDS, true) || filter_var($address, FILTER_VALIDATE_IP) === false) {
            return;
        }
        $line = 'auth failure from '.inet_ntop((string) inet_pton($address)).' kind='.$kind;
        if ($this->write !== null) {
            ($this->write)($line);

            return;
        }
        openlog(self::IDENT, LOG_PID, LOG_AUTHPRIV);
        syslog(LOG_WARNING, $line);
        closelog();
    }
}
