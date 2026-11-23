<?php

namespace App\Support;

use Symfony\Component\HttpFoundation\IpUtils;

/**
 * The SMTP host rule (R05-D11, OQ7): a private address such as a local
 * relay is fine, a link-local one (cloud metadata) never is. Checked when
 * the owner saves the host and again before each connection, since a name
 * can resolve elsewhere later.
 */
class SmtpHost
{
    public const LINK_LOCAL = ['169.254.0.0/16', 'fe80::/10'];

    /**
     * The addresses $host is or resolves to now; none when it doesn't resolve.
     *
     * @return list<string>
     */
    public function addresses(string $host): array
    {
        $host = trim($host, '[]');
        if (filter_var($host, FILTER_VALIDATE_IP) !== false) {
            return [$host];
        }
        $v4 = gethostbynamel($host) ?: [];
        $v6 = array_column(@dns_get_record($host, DNS_AAAA) ?: [], 'ipv6');

        return [...$v4, ...$v6];
    }

    /** Why $host is refused, in words; null when it may be used. */
    public function refusal(string $host): ?string
    {
        foreach ($this->addresses($host) as $address) {
            if (IpUtils::checkIp($address, self::LINK_LOCAL)) {
                return __('zimam.notify.smtp.link_local', ['address' => $address]);
            }
        }

        return null;
    }
}
