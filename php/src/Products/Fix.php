<?php
declare(strict_types=1);

namespace LinearJeph\Products;

/** fixops: AI fix jobs, projects and migrations. */
final class Fix
{
    private const BASE = '/api/v1/fixops';

    /** A wait ends on these. pr_open counts: the work is finished and only a person's decision is left. */
    public const FINAL_STATUSES = ['done', 'accepted', 'failed', 'rejected', 'cancelled', 'pr_open'];
    public const FAILED_STATUSES = ['failed', 'rejected', 'cancelled'];

    public function __construct(private readonly Client $c)
    {
    }

    public function overview(): array
    {
        return $this->c->get(self::BASE . '/overview');
    }

    /** @param array<string,string> $filters product, kind, status, project */
    public function jobs(array $filters = []): array
    {
        return $this->c->get(self::BASE . '/jobs', $filters);
    }

    /** @param array<string,mixed> $body source_id, issue, title, logs, repro_command, test_command, url, run_on, server_ref */
    public function createJob(array $body): array
    {
        return $this->c->post(self::BASE . '/jobs', $body);
    }

    public function job(int $id): array
    {
        return $this->c->get(self::BASE . "/jobs/$id");
    }

    public function jobEvents(int $id, int $after = 0): array
    {
        return $this->c->get(self::BASE . "/jobs/$id/events", ['after' => $after]);
    }

    public function cancelJob(int $id): array
    {
        return $this->c->post(self::BASE . "/jobs/$id/cancel");
    }

    public function acceptJob(int $id): array
    {
        return $this->c->post(self::BASE . "/jobs/$id/accept");
    }

    public function rejectJob(int $id, string $reason = ''): array
    {
        return $this->c->post(self::BASE . "/jobs/$id/reject", ['reason' => $reason]);
    }

    public function rollbackJob(int $id): array
    {
        return $this->c->post(self::BASE . "/jobs/$id/rollback");
    }

    /** The unified diff of a job. */
    public function jobPatch(int $id): string
    {
        return $this->c->download(self::BASE . "/jobs/$id/patch");
    }

    public function projects(): array
    {
        return $this->c->get(self::BASE . '/projects');
    }

    /** @param array<string,mixed> $body source_id, name, review_enabled, deps_enabled, tests_enabled, ... */
    public function createProject(array $body): array
    {
        return $this->c->post(self::BASE . '/projects', $body);
    }

    public function project(int $id): array
    {
        return $this->c->get(self::BASE . "/projects/$id");
    }

    public function updateProject(int $id, array $body): array
    {
        return $this->c->request('PATCH', self::BASE . "/projects/$id", $body);
    }

    /** Runs the code review poll now. */
    public function review(int $project): array
    {
        return $this->c->post(self::BASE . "/projects/$project/review-now");
    }

    /** Queues a dependency update job. */
    public function deps(int $project): array
    {
        return $this->c->post(self::BASE . "/projects/$project/deps-now");
    }

    /** Queues a test writing job. */
    public function tests(int $project, ?int $maxFiles = null): array
    {
        return $this->c->post(self::BASE . "/projects/$project/tests-now", $maxFiles === null ? null : ['max_files' => $maxFiles]);
    }

    public function migrations(): array
    {
        return $this->c->get(self::BASE . '/migrations');
    }

    public function migration(int $id): array
    {
        return $this->c->get(self::BASE . "/migrations/$id");
    }

    public function migrationPlan(int $id): array
    {
        return $this->c->post(self::BASE . "/migrations/$id/plan");
    }

    /**
     * Polls a job until it reaches a final status (see FINAL_STATUSES) and returns it.
     *
     * @throws \RuntimeException on timeout
     */
    public function wait(int $jobId, float $timeout = 1800.0): array
    {
        return $this->c->pollUntil($timeout, function () use ($jobId): array {
            $job = $this->job($jobId);
            return [$job, in_array($job['status'] ?? '', self::FINAL_STATUSES, true)];
        });
    }
}
