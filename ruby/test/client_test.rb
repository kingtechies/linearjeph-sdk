# frozen_string_literal: true

require_relative "test_helper"

class ClientTest < Minitest::Test
  include CaptureHelper

  def test_batches_gzip_hash_and_auth
    c = client
    250.times { |i| c.track("thing", uid: "user-1", props: { "i" => i }) }
    assert c.close
    reqs = @srv.list.select { |r| r.path == "/ingest/v1/prophecy/events" }
    assert_equal 250, reqs.sum { |r| r.body["events"].size }
    assert(reqs.all? { |r| r.body["events"].size <= 100 })
    assert(reqs.all?(&:gzip))
    assert_equal "lj_app_x", reqs[0].headers["x-lj-key"]
    assert_nil reqs[0].headers["authorization"]
    e = reqs[0].body["events"][0]
    assert_equal Digest::SHA256.hexdigest("user-1"), e["uid"]
    assert_equal "custom", e["kind"]
    assert_match(/\A\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z\z/, e["ts"])
    assert_match(%r{\Alinearjeph-prophecy-ruby/1\.3\.0}, reqs[0].headers["user-agent"])
  end

  def test_background_thread_sends_at_batch_size_without_flush
    c = client
    100.times { c.track("x") }
    deadline = Time.now + 5
    sleep 0.02 until @srv.events.size >= 100 || Time.now > deadline
    assert_equal 100, @srv.events.size
    c.close
  end

  def test_background_thread_sends_on_interval
    c = client(flush_interval: 0.1)
    c.track("x")
    deadline = Time.now + 5
    sleep 0.02 until !@srv.events.empty? || Time.now > deadline
    assert_equal 1, @srv.events.size
  end

  def test_hash_disabled_and_helpers
    c = client(hash_user_ids: false)
    c.feature("export", "raw-id")
    c.signup("u1", plan: "free", channel: "ads", country: "NG")
    c.payment("u1", 49.5, "usd", plan: "pro")
    c.payment("u1", -10)
    c.subscription("u1", plan: "pro", mrr: 49.5, status: "active")
    c.churn("u1", "price")
    c.identify("u1", { "company" => "Acme", "plan" => "pro" })
    c.close
    by = @srv.events.to_h { |e| [e["kind"], e] }
    assert_equal "raw-id", by["feature"]["uid"]
    assert_equal "export", by["feature"]["feature"]
    assert_equal 49.5, by["payment"]["amount"]
    assert_equal "USD", by["payment"]["currency"]
    assert_equal(-10, by["refund"]["amount"])
    assert_equal "USD", by["refund"]["currency"]
    assert_equal({ "plan" => "pro", "mrr" => 49.5, "status" => "active" }, by["subscription"]["props"])
    assert_equal "price", by["churn"]["props"]["reason"]
    assert_equal "NG", by["signup"]["props"]["country"]
    custs = @srv.list.select { |r| r.path == "/ingest/v1/prophecy/identify" }.flat_map { |r| r.body["customers"] }
    assert_equal 3, custs.size
    assert_equal "pro", custs[2]["plan"]
    assert_equal({ "company" => "Acme" }, custs[2]["traits"])
    assert custs[0]["signup_at"]
  end

  def test_retries_on_5xx
    @srv.fail_next = 2
    c = client
    c.track("x")
    c.close
    assert_equal 1, @srv.events.size
  end

  def test_queue_bounded
    c = client(queue_size: 10, start_thread: false)
    25.times { c.track("x") }
    assert_equal 15, c.dropped
  end

  def test_never_raises
    bad = Linearjeph::Prophecy::Client.new(ingest_key: "k", endpoint: "http://127.0.0.1:1", retries: 0, timeout: 1, flush_interval: 3600)
    bad.track("")
    bad.track("x", props: { "p" => Object.new.tap { |o| def o.to_json(*) = raise("boom") } })
    bad.track("x", amount: "abc", ts: "not a date at all")
    bad.track(nil)
    bad.identify(nil)
    bad.signup(nil)
    bad.payment("u", nil)
    bad.record_request(nil, nil, nil, nil)
    bad.flush
    bad.close
    bad.track("after-close")
    Linearjeph::Prophecy.client = nil
    Linearjeph::Prophecy.track("x")
    Linearjeph::Prophecy.feature("x")
    Linearjeph::Prophecy.flush
    assert_raises(Linearjeph::Prophecy::Error) { Linearjeph::Prophecy.report }
    assert_raises(ArgumentError) { Linearjeph::Prophecy::Client.new }
  end

  def test_secret_api
    c = client
    assert_equal 71, c.report("revenue")["report"]["headline"]["score"]
    assert_equal true, c.refresh["queued"]
    assert_equal 3, c.live["active_5m"]
    assert_equal "https://linearjeph.cc/prophecy-embed/tok", c.embed_url(600)
    reqs = @srv.list
    assert_equal ["/api/v1/prophecy/sdk/report?section=revenue", "/api/v1/prophecy/sdk/refresh", "/api/v1/prophecy/sdk/live", "/api/v1/prophecy/sdk/embed"], reqs.map(&:uri)
    assert_equal "Bearer ljp_sk_secret", reqs[0].headers["authorization"]
    assert_nil reqs[0].headers["x-lj-key"]
    assert_equal 600, reqs[3].body["ttl"]
    assert_raises(Linearjeph::Prophecy::Error) { client(secret_key: "").report }
  end

  def test_import_batching
    c = client
    rows = Array.new(12_000) { |i| { "uid" => "u", "name" => "x", "ts" => "2024-01-01T00:00:00Z", "id" => i } }
    prog = []
    res = c.import_history("events", rows) { |d, t| prog << [d, t] }
    reqs = @srv.list
    assert_equal [5000, 5000, 2000], reqs.map { |r| r.body["rows"].size }
    assert_equal Digest::SHA256.hexdigest("u"), reqs[0].body["rows"][0]["uid"]
    assert_equal "events", reqs[0].body["kind"]
    assert_equal [[5000, 12_000], [10_000, 12_000], [12_000, 12_000]], prog
    assert_equal 12_000, res["accepted"]
    assert_raises(Linearjeph::Prophecy::Error) { c.import_history("nope", []) }
  end

  def test_manifest
    client.send_manifest({ "schema" => 1, "root_name" => "x" })
    r = @srv.list.first
    assert_equal "/ingest/v1/prophecy/manifest", r.path
    assert_equal "x", r.body["manifest"]["root_name"]
    assert_equal "lj_app_x", r.headers["x-lj-key"]
  end

  def test_init_module_and_at_exit_flush
    Linearjeph::Prophecy.init(ingest_key: "k", endpoint: @srv.endpoint, flush_interval: 3600, backoff: 0.001)
    Linearjeph::Prophecy.payment("u", 5, "USD")
    assert Linearjeph::Prophecy.close
    assert_equal 1, @srv.events.size
    Linearjeph::Prophecy.client = nil
  end
end
