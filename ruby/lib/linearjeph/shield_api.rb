# frozen_string_literal: true

module Linearjeph
  # shield: vibe scans, secret watch, pentest, WAF, phishing and site security scans.
  class ShieldApi
    BASE = "/api/v1/shield"
    SECURITY = "/api/v1/security"
    RANK = { "info" => 0, "low" => 1, "medium" => 2, "high" => 3, "critical" => 4 }.freeze

    def initialize(client) = @c = client

    # Findings of a scan detail at or above fail_on. Raises ArgumentError for an unknown severity.
    def gate(scan, fail_on: "high")
      min = RANK[fail_on.to_s.downcase] or raise ArgumentError, "fail_on must be one of low, medium, high, critical (got #{fail_on.inspect})"
      (scan["findings"] || []).select { |f| f.is_a?(Hash) && RANK.fetch(f["severity"].to_s.downcase, -1) >= min }
    end

    def overview = @c.get("#{BASE}/overview")

    def vibe_projects = @c.get("#{BASE}/vibe/projects")
    def vibe_scan(project_id) = @c.post("#{BASE}/vibe/projects/#{project_id}/scan")
    def vibe_scan_detail(scan_id) = @c.get("#{BASE}/vibe/scans/#{scan_id}")

    # Polls a vibe scan until done or failed and returns the detail with findings.
    def wait_vibe_scan(scan_id, timeout: 1200)
      @c.poll_until(timeout) do
        d = vibe_scan_detail(scan_id)
        [d, %w[done failed].include?(d["status"])]
      end
    end

    def secrets = @c.get("#{BASE}/secrets")
    def add_secret_source(kind, ref) = @c.post("#{BASE}/secrets/sources", "kind" => kind, "ref" => ref)
    def secret_scan(source_id) = @c.post("#{BASE}/secrets/sources/#{source_id}/scan")
    # status: nil or open, acknowledged, revoked, false_positive
    def secret_findings(status = nil) = @c.get("#{BASE}/secrets/findings", status ? { "status" => status } : nil)

    def pentest = @c.get("#{BASE}/pentest")
    def pentest_scan(target_id) = @c.post("#{BASE}/pentest/targets/#{target_id}/scan")
    def pentest_scan_detail(scan_id) = @c.get("#{BASE}/pentest/scans/#{scan_id}")
    def pentest_history(target_id) = @c.get("#{BASE}/pentest/targets/#{target_id}/history")
    def pentest_retest(finding_id) = @c.post("#{BASE}/pentest/findings/#{finding_id}/retest")

    def wait_pentest_scan(scan_id, timeout: 1200)
      @c.poll_until(timeout) do
        d = pentest_scan_detail(scan_id)
        [d, %w[done failed].include?(d["status"])]
      end
    end

    def waf = @c.get("#{BASE}/waf")
    def waf_events(domain_id) = @c.get("#{BASE}/waf/domains/#{domain_id}/events")

    def phishing = @c.get("#{BASE}/phishing")
    def phishing_campaign(id) = @c.get("#{BASE}/phishing/campaigns/#{id}")
    def create_phishing_campaign(body = nil, **kw) = @c.post("#{BASE}/phishing/campaigns", body || kw)

    def security_overview = @c.get("#{SECURITY}/overview")
    def security_scan(target_id) = @c.post("#{SECURITY}/scans", "target" => target_id)
    def security_scan_detail(scan_id) = @c.get("#{SECURITY}/scans/#{scan_id}")
  end
end
