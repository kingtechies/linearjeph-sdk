<?php
declare(strict_types=1);

namespace LinearJeph\Products;

/** shield: vibe scans, secret watch, pentest, WAF, phishing and site security scans. */
final class Shield
{
    private const BASE = '/api/v1/shield';
    private const SECURITY = '/api/v1/security';
    private const RANK = ['info' => 0, 'low' => 1, 'medium' => 2, 'high' => 3, 'critical' => 4];

    public function __construct(private readonly Client $c)
    {
    }

    /**
     * Findings of a scan detail at or above $failOn.
     *
     * @param array<string,mixed> $scan a scan detail with a 'findings' list
     * @return list<array<string,mixed>>
     * @throws \InvalidArgumentException for an unknown severity
     */
    public function gate(array $scan, string $failOn = 'high'): array
    {
        $min = self::RANK[strtolower($failOn)] ?? null;
        if ($min === null) {
            throw new \InvalidArgumentException("failOn must be one of low, medium, high, critical (got '$failOn')");
        }
        $out = [];
        foreach ($scan['findings'] ?? [] as $f) {
            if (is_array($f) && (self::RANK[strtolower((string) ($f['severity'] ?? ''))] ?? -1) >= $min) {
                $out[] = $f;
            }
        }
        return $out;
    }

    public function overview(): array
    {
        return $this->c->get(self::BASE . '/overview');
    }

    // vibe
    public function vibeProjects(): array
    {
        return $this->c->get(self::BASE . '/vibe/projects');
    }

    public function vibeScan(int $project): array
    {
        return $this->c->post(self::BASE . "/vibe/projects/$project/scan");
    }

    public function vibeScanDetail(int $scan): array
    {
        return $this->c->get(self::BASE . "/vibe/scans/$scan");
    }

    /** Polls a vibe scan until done or failed and returns the detail with findings. */
    public function waitVibeScan(int $scan, float $timeout = 1200.0): array
    {
        return $this->c->pollUntil($timeout, function () use ($scan): array {
            $d = $this->vibeScanDetail($scan);
            return [$d, in_array($d['status'] ?? '', ['done', 'failed'], true)];
        });
    }

    // secret watch
    public function secrets(): array
    {
        return $this->c->get(self::BASE . '/secrets');
    }

    public function addSecretSource(string $kind, string $ref): array
    {
        return $this->c->post(self::BASE . '/secrets/sources', ['kind' => $kind, 'ref' => $ref]);
    }

    public function secretScan(int $source): array
    {
        return $this->c->post(self::BASE . "/secrets/sources/$source/scan");
    }

    /** @param string $status '' or open, acknowledged, revoked, false_positive */
    public function secretFindings(string $status = ''): array
    {
        return $this->c->get(self::BASE . '/secrets/findings', $status === '' ? [] : ['status' => $status]);
    }

    // pentest
    public function pentest(): array
    {
        return $this->c->get(self::BASE . '/pentest');
    }

    public function pentestScan(int $target): array
    {
        return $this->c->post(self::BASE . "/pentest/targets/$target/scan");
    }

    public function pentestScanDetail(int $scan): array
    {
        return $this->c->get(self::BASE . "/pentest/scans/$scan");
    }

    public function pentestHistory(int $target): array
    {
        return $this->c->get(self::BASE . "/pentest/targets/$target/history");
    }

    public function pentestRetest(int $finding): array
    {
        return $this->c->post(self::BASE . "/pentest/findings/$finding/retest");
    }

    public function waitPentestScan(int $scan, float $timeout = 1200.0): array
    {
        return $this->c->pollUntil($timeout, function () use ($scan): array {
            $d = $this->pentestScanDetail($scan);
            return [$d, in_array($d['status'] ?? '', ['done', 'failed'], true)];
        });
    }

    // waf
    public function waf(): array
    {
        return $this->c->get(self::BASE . '/waf');
    }

    public function wafEvents(int $domain): array
    {
        return $this->c->get(self::BASE . "/waf/domains/$domain/events");
    }

    // phishing
    public function phishing(): array
    {
        return $this->c->get(self::BASE . '/phishing');
    }

    public function phishingCampaign(int $id): array
    {
        return $this->c->get(self::BASE . "/phishing/campaigns/$id");
    }

    public function createPhishingCampaign(array $body): array
    {
        return $this->c->post(self::BASE . '/phishing/campaigns', $body);
    }

    // security (site scans)
    public function securityOverview(): array
    {
        return $this->c->get(self::SECURITY . '/overview');
    }

    public function securityScan(int $target): array
    {
        return $this->c->post(self::SECURITY . '/scans', ['target' => $target]);
    }

    public function securityScanDetail(int $scan): array
    {
        return $this->c->get(self::SECURITY . "/scans/$scan");
    }
}
