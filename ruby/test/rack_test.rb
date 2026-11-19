# frozen_string_literal: true

require_relative "test_helper"

class RackTest < Minitest::Test
  include CaptureHelper

  def test_normalize
    n = Linearjeph::Prophecy::Rack.method(:normalize)
    assert_equal "/users/:id/orders/:id", n.call("/users/42/orders/7?x=1")
    assert_equal "/f/:id", n.call("/f/3f2b8c1e-1234-4abc-9def-0123456789ab")
    assert_equal "/t/:id", n.call("/t/0123456789abcdef0123")
    assert_equal "/about", n.call("/about")
    assert_equal "/", n.call("")
  end

  def test_records_route_status_duration
    c = client
    Linearjeph::Prophecy.client = c
    app = lambda do |env|
      case env["PATH_INFO"]
      when "/boom" then raise "boom"
      when "/rails/5" then env["action_dispatch.route_uri_pattern"] = "/rails/:id(.:format)"
      when "/sinatra/5" then env["sinatra.route"] = "GET /sinatra/:id"
      end
      [201, {}, ["ok"]]
    end
    mw = Linearjeph::Prophecy.middleware.new(app)
    assert_equal 201, mw.call("PATH_INFO" => "/users/123", "REQUEST_METHOD" => "get")[0]
    mw.call("PATH_INFO" => "/rails/5", "REQUEST_METHOD" => "GET")
    mw.call("PATH_INFO" => "/sinatra/5", "REQUEST_METHOD" => "POST")
    assert_raises(RuntimeError) { mw.call("PATH_INFO" => "/boom", "REQUEST_METHOD" => "GET") }
    c.close
    ev = @srv.events
    assert_equal 4, ev.size
    assert_equal "/users/:id", ev[0]["props"]["route"]
    assert_equal 201, ev[0]["props"]["status"]
    assert_equal "GET", ev[0]["props"]["method"]
    assert_kind_of Numeric, ev[0]["props"]["duration_ms"]
    assert_equal "request", ev[0]["kind"]
    refute ev[0].key?("uid")
    assert_equal "/rails/:id", ev[1]["props"]["route"]
    assert_equal "/sinatra/:id", ev[2]["props"]["route"]
    assert_equal 500, ev[3]["props"]["status"]
  ensure
    Linearjeph::Prophecy.client = nil
  end

  def test_middleware_never_breaks_app_without_init
    Linearjeph::Prophecy.client = nil
    mw = Linearjeph::Prophecy::Rack.new(->(_env) { [200, {}, ["x"]] })
    assert_equal 200, mw.call("PATH_INFO" => "/", "REQUEST_METHOD" => "GET")[0]
  end
end
