<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

/**
 * A UFW rule Zimam owns (R06-D3), copied from firewall.status: the owner's
 * own, and the locked ones the agent keeps (one per SSH port, http, https),
 * which the panel never sends. The patterns and limits mirror the agent's
 * FirewallRule, anchored with \z.
 */
class FirewallRule extends Model
{
    public const KEY_PATTERN = '/^[a-z][a-z0-9-]{0,22}[a-z0-9]\z/';

    public const ACTIONS = ['allow', 'deny', 'limit'];

    public const PROTOS = ['tcp', 'udp'];

    /** The agent's caps: rules per list, sources per rule. */
    public const MAX_RULES = 100;

    public const MAX_SOURCES = 20;

    protected $fillable = ['key', 'action', 'proto', 'port_from', 'port_to', 'from', 'locked'];

    protected function casts(): array
    {
        return ['from' => 'array', 'locked' => 'boolean', 'port_from' => 'integer', 'port_to' => 'integer'];
    }

    /** A key the agent keeps for itself (contract.LockedFirewallRule). */
    public static function isLockedKey(string $key): bool
    {
        return $key === 'http' || $key === 'https' || str_starts_with($key, 'ssh-');
    }

    /** "80", or "9000–9010" for a range. */
    public function ports(): string
    {
        return $this->port_from === $this->port_to ? (string) $this->port_from : "{$this->port_from}–{$this->port_to}";
    }

    /** @return array<string, mixed> the rule as firewall.rules takes it */
    public function spec(): array
    {
        return ['id' => $this->key, 'action' => $this->action, 'proto' => $this->proto, 'port_from' => $this->port_from, 'port_to' => $this->port_to, 'from' => array_values($this->from)];
    }

    /**
     * Makes the rows what firewall.status reports: its rules and locked rules.
     *
     * @param  array<string, mixed>  $status
     */
    public static function syncFrom(array $status): void
    {
        $keep = [];
        foreach ([[$status['rules'] ?? [], false], [$status['locked'] ?? [], true]] as [$rules, $locked]) {
            foreach ($rules as $r) {
                $keep[] = $r['id'];
                self::query()->updateOrCreate(['key' => $r['id']], [
                    'action' => $r['action'], 'proto' => $r['proto'], 'port_from' => $r['port_from'], 'port_to' => $r['port_to'],
                    'from' => array_values($r['from']), 'locked' => $locked,
                ]);
            }
        }
        self::query()->whereNotIn('key', $keep)->delete();
    }
}
