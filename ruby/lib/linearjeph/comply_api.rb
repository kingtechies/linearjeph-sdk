# frozen_string_literal: true

require "erb"

module Linearjeph
  # comply: compliance frameworks, tasks, re-checks and the audit export.
  class ComplyApi
    BASE = "/api/v1/comply"

    def initialize(client) = @c = client

    def overview = @c.get("#{BASE}/overview")
    def compliance = @c.get("#{BASE}/compliance")
    def framework(key) = @c.get("#{BASE}/compliance/frameworks/#{ERB::Util.url_encode(key.to_s)}")
    def enable_framework(key) = @c.post("#{BASE}/compliance/frameworks", "key" => key)
    def tasks = @c.get("#{BASE}/compliance/tasks")
    def recheck = @c.post("#{BASE}/compliance/recheck")
    # The audit package (a zip), as a binary String.
    def export = @c.download("#{BASE}/compliance/export")
  end
end
