# frozen_string_literal: true

module Linearjeph
  # ops: on-call summary, servers, incidents, cost, managed databases and backup targets.
  class OpsApi
    BASE = "/api/v1/opsagent"
    BACKUPS = "/api/v1/backups/agent"

    def initialize(client) = @c = client

    def summary = @c.get("#{BASE}/summary")
    def servers = @c.get("#{BASE}/servers")
    # status: nil, "open" or "resolved"
    def incidents(status = nil) = @c.get("#{BASE}/incidents", status ? { "status" => status } : nil)
    def incident(id) = @c.get("#{BASE}/incidents/#{id}")
    def resolve_incident(id) = @c.post("#{BASE}/incidents/#{id}/resolve")
    def cost_summary = @c.get("#{BASE}/cost")
    def cost_analyse = @c.post("#{BASE}/cost/analyse")
    def managed_databases = @c.get("#{BASE}/managed-db")
    def backup_targets = @c.get("#{BACKUPS}/targets")
    def run_backup(target_id) = @c.post("#{BACKUPS}/targets/#{target_id}/run")
    def run_drill(target_id) = @c.post("#{BACKUPS}/targets/#{target_id}/drill")
    def backup_runs(target_id) = @c.get("#{BACKUPS}/targets/#{target_id}/runs")
  end
end
