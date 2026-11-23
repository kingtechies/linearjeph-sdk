<?php
declare(strict_types=1);

namespace LinearJeph\Products;

/** ops: on-call summary, servers, incidents, cost, managed databases and backup targets. */
final class Ops
{
    private const BASE = '/api/v1/opsagent';
    private const BACKUPS = '/api/v1/backups/agent';

    public function __construct(private readonly Client $c)
    {
    }

    public function summary(): array
    {
        return $this->c->get(self::BASE . '/summary');
    }

    public function servers(): array
    {
        return $this->c->get(self::BASE . '/servers');
    }

    /** @param string $status '' or 'open' or 'resolved' */
    public function incidents(string $status = ''): array
    {
        return $this->c->get(self::BASE . '/incidents', $status === '' ? [] : ['status' => $status]);
    }

    public function incident(int $id): array
    {
        return $this->c->get(self::BASE . "/incidents/$id");
    }

    public function resolveIncident(int $id): array
    {
        return $this->c->post(self::BASE . "/incidents/$id/resolve");
    }

    public function costSummary(): array
    {
        return $this->c->get(self::BASE . '/cost');
    }

    public function costAnalyse(): array
    {
        return $this->c->post(self::BASE . '/cost/analyse');
    }

    public function managedDatabases(): array
    {
        return $this->c->get(self::BASE . '/managed-db');
    }

    public function backupTargets(): array
    {
        return $this->c->get(self::BACKUPS . '/targets');
    }

    public function runBackup(int $target): array
    {
        return $this->c->post(self::BACKUPS . "/targets/$target/run");
    }

    public function runDrill(int $target): array
    {
        return $this->c->post(self::BACKUPS . "/targets/$target/drill");
    }

    public function backupRuns(int $target): array
    {
        return $this->c->get(self::BACKUPS . "/targets/$target/runs");
    }
}
