<?php
declare(strict_types=1);

namespace LinearJeph\Products;

/** comply: compliance frameworks, tasks, re-checks and the audit export. */
final class Comply
{
    private const BASE = '/api/v1/comply';

    public function __construct(private readonly Client $c)
    {
    }

    public function overview(): array
    {
        return $this->c->get(self::BASE . '/overview');
    }

    public function compliance(): array
    {
        return $this->c->get(self::BASE . '/compliance');
    }

    public function framework(string $key): array
    {
        return $this->c->get(self::BASE . '/compliance/frameworks/' . rawurlencode($key));
    }

    public function enableFramework(string $key): array
    {
        return $this->c->post(self::BASE . '/compliance/frameworks', ['key' => $key]);
    }

    public function tasks(): array
    {
        return $this->c->get(self::BASE . '/compliance/tasks');
    }

    public function recheck(): array
    {
        return $this->c->post(self::BASE . '/compliance/recheck');
    }

    /** The audit package (a zip). */
    public function export(): string
    {
        return $this->c->download(self::BASE . '/compliance/export');
    }
}
