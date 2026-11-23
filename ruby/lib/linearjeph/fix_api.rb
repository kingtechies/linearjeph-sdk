# frozen_string_literal: true

module Linearjeph
  # fixops: AI fix jobs, projects and migrations.
  class FixApi
    BASE = "/api/v1/fixops"
    # A wait ends on these. pr_open counts: the work is finished and only a person's decision is left.
    FINAL_STATUSES = %w[done accepted failed rejected cancelled pr_open].freeze
    FAILED_STATUSES = %w[failed rejected cancelled].freeze

    def initialize(client) = @c = client

    def overview = @c.get("#{BASE}/overview")

    # filters: product, kind, status, project
    def jobs(filters = {}) = @c.get("#{BASE}/jobs", filters)

    # body keys: source_id, issue, title, logs, repro_command, test_command, url, run_on, server_ref
    def create_job(body = nil, **kw) = @c.post("#{BASE}/jobs", body || kw)
    def job(id) = @c.get("#{BASE}/jobs/#{id}")
    def job_events(id, after: 0) = @c.get("#{BASE}/jobs/#{id}/events", "after" => after)
    def cancel_job(id) = @c.post("#{BASE}/jobs/#{id}/cancel")
    def accept_job(id) = @c.post("#{BASE}/jobs/#{id}/accept")
    def reject_job(id, reason = "") = @c.post("#{BASE}/jobs/#{id}/reject", "reason" => reason)
    def rollback_job(id) = @c.post("#{BASE}/jobs/#{id}/rollback")

    # The unified diff of a job.
    def job_patch(id) = @c.download("#{BASE}/jobs/#{id}/patch")

    def projects = @c.get("#{BASE}/projects")
    def create_project(body = nil, **kw) = @c.post("#{BASE}/projects", body || kw)
    def project(id) = @c.get("#{BASE}/projects/#{id}")
    def update_project(id, body = nil, **kw) = @c.request("PATCH", "#{BASE}/projects/#{id}", body || kw)
    # Runs the code review poll now.
    def review(project_id) = @c.post("#{BASE}/projects/#{project_id}/review-now")
    # Queues a dependency update job.
    def deps(project_id) = @c.post("#{BASE}/projects/#{project_id}/deps-now")
    # Queues a test writing job.
    def tests(project_id, max_files: nil) = @c.post("#{BASE}/projects/#{project_id}/tests-now", max_files ? { "max_files" => max_files } : nil)

    def migrations = @c.get("#{BASE}/migrations")
    def migration(id) = @c.get("#{BASE}/migrations/#{id}")
    def migration_plan(id) = @c.post("#{BASE}/migrations/#{id}/plan")

    # Polls a job until it reaches a final status (FINAL_STATUSES) and returns it. Raises TimeoutError.
    def wait(job_id, timeout: 1800)
      @c.poll_until(timeout) do
        j = job(job_id)
        [j, FINAL_STATUSES.include?(j["status"])]
      end
    end
  end
end
