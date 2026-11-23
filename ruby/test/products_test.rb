# frozen_string_literal: true

require_relative "test_helper"

# Scripted fake product API on a TCPServer. script maps "METHOD /path" to a list of [status, body, headers];
# each entry is used once and the last repeats. Unscripted routes answer 200 {"results":[]}.
class ProductsServer
  Rec = Struct.new(:method, :uri, :path, :headers, :body)

  attr_reader :port

  def initialize
    @server = TCPServer.new("127.0.0.1", 0)
    @port = @server.addr[1]
    @lock = Mutex.new
    @requests = []
    @script = {}
    @counts = Hash.new(0)
    @thread = Thread.new { serve }
    @thread.report_on_exception = false
  end

  def base_url = "http://127.0.0.1:#{@port}"

  def script(map)
    @lock.synchronize do
      @script = map
      @counts = Hash.new(0)
      @requests = []
    end
  end

  def requests = @lock.synchronize { @requests.dup }

  def stop
    @server.close
    @thread.kill
  end

  private

  def serve
    loop { Thread.new(@server.accept) { |s| handle(s) } }
  rescue IOError, Errno::EBADF
    nil
  end

  def handle(sock)
    line = sock.gets or return
    method, uri, = line.split(" ")
    headers = {}
    while (l = sock.gets) && l != "\r\n"
      k, v = l.split(":", 2)
      headers[k.downcase] = v.strip
    end
    raw = (n = headers["content-length"].to_i).positive? ? sock.read(n) : ""
    path = uri.split("?").first
    status, body, extra = reply("#{method} #{path}", Rec.new(method, uri, path, headers, raw.empty? ? nil : JSON.parse(raw)))
    body = body.is_a?(String) ? body : JSON.generate(body)
    head = extra.map { |k, v| "#{k}: #{v}\r\n" }.join
    sock.write "HTTP/1.1 #{status} X\r\n#{head}Content-Type: #{extra['Content-Type'] || 'application/json'}\r\n" \
               "Content-Length: #{body.bytesize}\r\nConnection: close\r\n\r\n#{body}"
  rescue StandardError
    nil
  ensure
    sock.close
  end

  def reply(key, rec)
    @lock.synchronize do
      @requests << rec
      entries = @script[key] or return [200, { "results" => [] }, {}]
      entry = entries[[@counts[key], entries.size - 1].min]
      @counts[key] += 1
      [entry[0], entry[1], entry[2] || {}]
    end
  end
end

class ProductsTest < Minitest::Test
  FAKE_KEY = "ljk_" + "test1234_" + ("x" * 32)

  def setup
    @srv = ProductsServer.new
  end

  def teardown
    @srv.stop
  end

  def lj(**o)
    Linearjeph::Client.new(**{ api_key: FAKE_KEY, base_url: @srv.base_url, backoff: 0.001, poll_interval: 0.01 }.merge(o))
  end

  def test_constant_alias_and_version
    assert_same Linearjeph::Client, LinearJeph::Client
    assert_equal "1.3.0", Linearjeph::VERSION
  end

  def test_requires_key_and_reads_env
    old = ENV.delete("LJ_API_KEY")
    assert_raises(ArgumentError) { Linearjeph::Client.new }
    ENV["LJ_API_KEY"] = FAKE_KEY
    ENV["LJ_BASE_URL"] = "http://example.invalid/"
    assert_instance_of Linearjeph::Client, Linearjeph::Client.new
  ensure
    ENV.delete("LJ_BASE_URL")
    old ? ENV["LJ_API_KEY"] = old : ENV.delete("LJ_API_KEY")
  end

  def test_headers_and_whoami
    @srv.script("GET /api/v1/apikeys/whoami" => [[200, { "organization" => { "id" => 1, "name" => "Acme" }, "scopes" => ["fix"] }]])
    who = lj.whoami
    r = @srv.requests.first
    assert_equal "Acme", who["organization"]["name"]
    assert_equal "Bearer #{FAKE_KEY}", r.headers["authorization"]
    assert_equal "linearjeph-ruby/1.3.0", r.headers["user-agent"]
  end

  def test_api_error
    @srv.script("GET /api/v1/fixops/jobs/9" => [[404, { "error" => { "code" => "not_found", "message" => "Job not found." } }]])
    e = assert_raises(Linearjeph::ApiError) { lj.fix.job(9) }
    assert_equal 404, e.status
    assert_equal "not_found", e.code
    assert_equal "Job not found.", e.message
    assert_equal 1, @srv.requests.size
  end

  def test_get_retries_and_honours_retry_after
    @srv.script("GET /api/v1/fixops/overview" => [
                  [503, { "error" => { "code" => "busy", "message" => "x" } }, { "Retry-After" => "0" }], [429, {}], [200, { "ok" => true }]
                ])
    assert_equal true, lj.fix.overview["ok"]
    assert_equal 3, @srv.requests.size
  end

  def test_get_gives_up_after_three_tries
    @srv.script("GET /api/v1/fixops/overview" => [[500, { "error" => { "code" => "boom", "message" => "x" } }]])
    e = assert_raises(Linearjeph::ApiError) { lj.fix.overview }
    assert_equal 500, e.status
    assert_equal 3, @srv.requests.size
  end

  def test_post_is_not_retried
    @srv.script("POST /api/v1/fixops/jobs/3/cancel" => [[500, { "error" => { "code" => "boom", "message" => "x" } }]])
    assert_raises(Linearjeph::ApiError) { lj.fix.cancel_job(3) }
    assert_equal 1, @srv.requests.size
  end

  def test_network_error
    e = assert_raises(Linearjeph::ApiError) { Linearjeph::Client.new(api_key: FAKE_KEY, base_url: "http://127.0.0.1:1", retries: 0, timeout: 1).fix.overview }
    assert_equal 0, e.status
  end

  def test_injected_transport
    seen = nil
    t = lambda { |m, url, h, body, _to|
      seen = [m, url, h, body]
      [200, '{"ok":true}', {}]
    }
    c = Linearjeph::Client.new(api_key: FAKE_KEY, base_url: "http://x.test", org: 7, transport: t)
    assert_equal({ "ok" => true }, c.request("post", "/a/b", { "k" => 1 }, { "q" => "a b" }))
    assert_equal "POST", seen[0]
    assert_equal "http://x.test/a/b?q=a+b&org=7", seen[1]
    assert_equal '{"k":1}', seen[3]
    assert_equal "application/json", seen[2]["Content-Type"]
  end

  def test_route_map
    c = lj(org: 7)
    cases = [
      ["GET", "/api/v1/fixops/overview", -> { c.fix.overview }],
      ["POST", "/api/v1/fixops/jobs", -> { c.fix.create_job(source_id: "s", issue: "bug") }],
      ["GET", "/api/v1/fixops/jobs/3/events", -> { c.fix.job_events(3, after: 9) }],
      ["POST", "/api/v1/fixops/jobs/3/reject", -> { c.fix.reject_job(3, "no") }],
      ["POST", "/api/v1/fixops/jobs/3/rollback", -> { c.fix.rollback_job(3) }],
      ["PATCH", "/api/v1/fixops/projects/2", -> { c.fix.update_project(2, deps_enabled: true) }],
      ["POST", "/api/v1/fixops/projects/2/review-now", -> { c.fix.review(2) }],
      ["POST", "/api/v1/fixops/projects/2/deps-now", -> { c.fix.deps(2) }],
      ["POST", "/api/v1/fixops/projects/2/tests-now", -> { c.fix.tests(2, max_files: 4) }],
      ["POST", "/api/v1/fixops/migrations/5/plan", -> { c.fix.migration_plan(5) }],
      ["POST", "/api/v1/shield/vibe/projects/4/scan", -> { c.shield.vibe_scan(4) }],
      ["GET", "/api/v1/shield/vibe/scans/8", -> { c.shield.vibe_scan_detail(8) }],
      ["GET", "/api/v1/shield/secrets/findings", -> { c.shield.secret_findings("open") }],
      ["POST", "/api/v1/shield/pentest/targets/6/scan", -> { c.shield.pentest_scan(6) }],
      ["POST", "/api/v1/shield/pentest/findings/6/retest", -> { c.shield.pentest_retest(6) }],
      ["GET", "/api/v1/shield/waf/domains/6/events", -> { c.shield.waf_events(6) }],
      ["POST", "/api/v1/security/scans", -> { c.shield.security_scan(6) }],
      ["POST", "/api/v1/opsagent/incidents/2/resolve", -> { c.ops.resolve_incident(2) }],
      ["POST", "/api/v1/opsagent/cost/analyse", -> { c.ops.cost_analyse }],
      ["GET", "/api/v1/opsagent/managed-db", -> { c.ops.managed_databases }],
      ["GET", "/api/v1/backups/agent/targets", -> { c.ops.backup_targets }],
      ["POST", "/api/v1/backups/agent/targets/3/drill", -> { c.ops.run_drill(3) }],
      ["GET", "/api/v1/backups/agent/targets/3/runs", -> { c.ops.backup_runs(3) }],
      ["GET", "/api/v1/endpoints/endpoints", -> { c.models.endpoints }],
      ["POST", "/api/v1/endpoints/endpoints/1/suspend", -> { c.models.endpoint_action(1, "suspend") }],
      ["GET", "/api/v1/agentcloud/private/endpoints/1/logs", -> { c.models.private_logs(1) }],
      ["GET", "/api/v1/agentcloud/finetune/jobs", -> { c.models.finetune_jobs }],
      ["GET", "/api/v1/agentcloud/agents/1/runs/9", -> { c.agents.run(1, 9) }],
      ["POST", "/api/v1/agentcloud/agents/1/run", -> { c.agents.action(1, "run", { "q" => 1 }) }],
      ["POST", "/api/v1/comply/compliance/recheck", -> { c.comply.recheck }],
      ["GET", "/api/v1/comply/compliance/tasks", -> { c.comply.tasks }]
    ]
    cases.each { |_, _, fn| fn.call }
    got = @srv.requests
    cases.each_with_index do |(m, p, _), i|
      assert_equal [m, p], [got[i].method, got[i].path], "route #{i}"
    end
    assert_equal({ "source_id" => "s", "issue" => "bug" }, got[1].body)
    assert_equal "application/json", got[1].headers["content-type"]
    assert_includes got[2].uri, "org=7"
    assert_includes got[2].uri, "after=9"
    assert_equal({ "reason" => "no" }, got[3].body)
    assert_equal({ "max_files" => 4 }, got[8].body)
    assert_equal({ "input" => { "q" => 1 } }, got[28].body)
  end

  def test_generic_request_shapes
    @srv.script("GET /api/v1/market/catalog" => [[200, { "results" => [1] }]],
                "POST /api/v1/x/empty" => [[204, ""]],
                "GET /api/v1/x/text" => [[200, "not json\n", { "Content-Type" => "text/plain" }]],
                "GET /api/v1/x/list" => [[200, "[1,2]"]])
    c = lj
    assert_equal [1], c.request("get", "/api/v1/market/catalog", nil, { "q" => "a b" })["results"]
    assert_equal({}, c.request("POST", "/api/v1/x/empty", {}))
    assert_equal "not json\n", c.request("GET", "/api/v1/x/text")["raw"]
    assert_equal [1, 2], c.request("GET", "/api/v1/x/list")["data"]
    assert_includes @srv.requests.first.uri, "q=a+b"
  end

  def test_downloads
    @srv.script("GET /api/v1/fixops/jobs/2/patch" => [[200, "--- a\n+++ b\n", { "Content-Type" => "text/x-diff" }]])
    assert_equal "--- a\n+++ b\n", lj.fix.job_patch(2)
  end

  def test_fix_wait
    @srv.script("GET /api/v1/fixops/jobs/5" => [[200, { "id" => 5, "status" => "patching" }], [200, { "id" => 5, "status" => "testing" }],
                                                [200, { "id" => 5, "status" => "pr_open" }]])
    assert_equal "pr_open", lj.fix.wait(5, timeout: 5)["status"]
    assert_equal 3, @srv.requests.size
  end

  def test_fix_wait_times_out
    @srv.script("GET /api/v1/fixops/jobs/5" => [[200, { "id" => 5, "status" => "patching" }]])
    assert_raises(Linearjeph::TimeoutError) { lj.fix.wait(5, timeout: 0.05) }
  end

  def test_shield_gate
    scan = { "findings" => [{ "severity" => "low" }, { "severity" => "high" }, { "severity" => "critical" }, { "severity" => "info" }] }
    s = lj.shield
    assert_equal 2, s.gate(scan).size
    assert_equal 1, s.gate(scan, fail_on: "critical").size
    assert_equal 3, s.gate(scan, fail_on: "low").size
    assert_equal 2, s.gate(scan, fail_on: "Medium").size
    assert_raises(ArgumentError) { s.gate(scan, fail_on: "severe") }
    assert_equal [], s.gate({})
  end

  def test_wait_vibe_scan
    @srv.script("GET /api/v1/shield/vibe/scans/8" => [[200, { "id" => 8, "status" => "running" }], [200, { "id" => 8, "status" => "done", "findings" => [] }]])
    assert_equal "done", lj.shield.wait_vibe_scan(8, timeout: 5)["status"]
  end
end
