# frozen_string_literal: true

module Linearjeph
  # agents: Agent Cloud.
  class AgentsApi
    BASE = "/api/v1/agentcloud"

    def initialize(client) = @c = client

    def overview = @c.get("#{BASE}/overview")
    def list = @c.get("#{BASE}/agents")
    def get(id) = @c.get("#{BASE}/agents/#{id}")
    def runs(id) = @c.get("#{BASE}/agents/#{id}/runs")
    def run(id, run_id) = @c.get("#{BASE}/agents/#{id}/runs/#{run_id}")

    # action: run, pause, resume, redeploy or rotate-webhook; input is only used by run
    def action(id, action, input = nil) = @c.post("#{BASE}/agents/#{id}/#{action}", input.nil? ? nil : { "input" => input })
  end
end
