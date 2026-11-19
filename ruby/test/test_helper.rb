# frozen_string_literal: true

require "minitest/autorun"
require "json"
require "socket"
require "zlib"
require "stringio"
require "tmpdir"
require "fileutils"
require "linearjeph"

# Minimal capture HTTP server on a TCPServer (no WEBrick). Records every request; `fail_next` answers 503 N times.
class CaptureServer
  Rec = Struct.new(:method, :uri, :path, :headers, :body, :gzip)

  attr_reader :port, :requests
  attr_accessor :fail_next

  def initialize
    @server = TCPServer.new("127.0.0.1", 0)
    @port = @server.addr[1]
    @requests = []
    @lock = Mutex.new
    @fail_next = 0
    @thread = Thread.new { serve }
    @thread.report_on_exception = false
  end

  def endpoint = "http://127.0.0.1:#{@port}"

  def list = @lock.synchronize { @requests.dup }

  def events
    list.select { |r| r.path == "/ingest/v1/prophecy/events" }.flat_map { |r| r.body["events"] }
  end

  def stop
    @server.close
    @thread.kill
  end

  private

  def serve
    loop do
      sock = @server.accept
      Thread.new(sock) { |s| handle(s) }
    end
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
    gz = headers["content-encoding"] == "gzip"
    raw = Zlib.gunzip(raw) if gz && !raw.empty?
    path = uri.split("?").first
    status, body = respond(path, raw.empty? ? {} : JSON.parse(raw), method, uri, headers, gz)
    sock.write "HTTP/1.1 #{status} X\r\nContent-Type: application/json\r\nContent-Length: #{body.bytesize}\r\nConnection: close\r\n\r\n#{body}"
  rescue StandardError
    nil
  ensure
    sock.close
  end

  def respond(path, body, method, uri, headers, gz)
    @lock.synchronize do
      if @fail_next.positive?
        @fail_next -= 1
        return [503, '{"error":"busy"}']
      end
      @requests << Rec.new(method, uri, path, headers, body, gz)
    end
    case path
    when "/api/v1/prophecy/sdk/embed" then [200, '{"url":"https://linearjeph.cc/prophecy-embed/tok","expires_at":"x"}']
    when "/api/v1/prophecy/sdk/import" then [200, JSON.generate("job" => 1, "accepted" => body["rows"].size, "rejected" => 0, "errors" => [])]
    when "/api/v1/prophecy/sdk/report" then [200, '{"report":{"headline":{"score":71}},"created_at":"x","stale":false}']
    when "/api/v1/prophecy/sdk/refresh" then [202, '{"queued":true}']
    when "/api/v1/prophecy/sdk/live" then [200, '{"active_5m":3}']
    else [202, '{"accepted":1,"rejected":0}']
    end
  end
end

module CaptureHelper
  def setup
    @srv = CaptureServer.new
    @clients = []
  end

  def teardown
    @clients.each { |c| c.close rescue nil } # rubocop:disable Style/RescueModifier
    @srv.stop
  end

  def client(**o)
    c = Linearjeph::Prophecy::Client.new(**{ ingest_key: "lj_app_x", secret_key: "ljp_sk_secret", endpoint: @srv.endpoint,
                                             backoff: 0.001, flush_interval: 3600 }.merge(o))
    @clients << c
    c
  end
end
