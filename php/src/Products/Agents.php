<?php
declare(strict_types=1);

namespace LinearJeph\Products;

/** agents: Agent Cloud. */
final class Agents
{
    private const BASE = '/api/v1/agentcloud';

    public function __construct(private readonly Client $c)
    {
    }

    public function overview(): array
    {
        return $this->c->get(self::BASE . '/overview');
    }

    public function list(): array
    {
        return $this->c->get(self::BASE . '/agents');
    }

    public function get(int $id): array
    {
        return $this->c->get(self::BASE . "/agents/$id");
    }

    public function runs(int $id): array
    {
        return $this->c->get(self::BASE . "/agents/$id/runs");
    }

    public function run(int $id, int $runId): array
    {
        return $this->c->get(self::BASE . "/agents/$id/runs/$runId");
    }

    /** @param string $action run, pause, resume, redeploy or rotate-webhook; $input is only used by run */
    public function action(int $id, string $action, mixed $input = null): array
    {
        return $this->c->post(self::BASE . "/agents/$id/$action", $input === null ? null : ['input' => $input]);
    }
}
