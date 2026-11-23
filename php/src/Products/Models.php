<?php
declare(strict_types=1);

namespace LinearJeph\Products;

/** models: AI endpoints, private endpoints and fine-tuning. */
final class Models
{
    private const ENDPOINTS = '/api/v1/endpoints/endpoints';
    private const PRIVATE = '/api/v1/agentcloud/private/endpoints';
    private const FINETUNE = '/api/v1/agentcloud/finetune';

    public function __construct(private readonly Client $c)
    {
    }

    public function endpoints(): array
    {
        return $this->c->get(self::ENDPOINTS);
    }

    public function endpoint(int $id): array
    {
        return $this->c->get(self::ENDPOINTS . "/$id");
    }

    public function endpointUsage(int $id): array
    {
        return $this->c->get(self::ENDPOINTS . "/$id/usage");
    }

    /** @param string $action suspend, resume or rotate-key */
    public function endpointAction(int $id, string $action): array
    {
        return $this->c->post(self::ENDPOINTS . "/$id/$action");
    }

    public function privateEndpoints(): array
    {
        return $this->c->get(self::PRIVATE);
    }

    public function privateEndpoint(int $id): array
    {
        return $this->c->get(self::PRIVATE . "/$id");
    }

    public function privateUsage(int $id): array
    {
        return $this->c->get(self::PRIVATE . "/$id/usage");
    }

    public function privateLogs(int $id): array
    {
        return $this->c->get(self::PRIVATE . "/$id/logs");
    }

    public function finetuneDatasets(): array
    {
        return $this->c->get(self::FINETUNE . '/datasets');
    }

    public function finetuneJobs(): array
    {
        return $this->c->get(self::FINETUNE . '/jobs');
    }

    public function finetuneJob(int $id): array
    {
        return $this->c->get(self::FINETUNE . "/jobs/$id");
    }

    /** @param array<string,mixed> $body dataset_id, base_model, method */
    public function createFinetuneJob(array $body): array
    {
        return $this->c->post(self::FINETUNE . '/jobs', $body);
    }
}
