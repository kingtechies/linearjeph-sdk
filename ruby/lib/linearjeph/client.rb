# frozen_string_literal: true

require "json"
require "net/http"
require "uri"
require "linearjeph/prophecy/version"
require "linearjeph/api_error"

module Linearjeph
  # SDK version (same as Prophecy::VERSION).
  VERSION = Prophecy::VERSION

  # Product API client: fix, shield, ops, models, agents, comply.
  #
  #   lj = Linearjeph::Client.new                # LJ_API_KEY and LJ_BASE_URL from the environment
  #   job = lj.fix.wait(lj.fix.create_job(source_id: "gh:acme/app", issue: "bug")["id"])
  #
  # GET requests retry 429, 5xx and network errors (3 tries, honours Retry-After); writes are never retried.
  # Answers are Hashes with string keys; lists of records are under "results".
  class Client
    DEFAULT_BASE_URL = "https://linearjeph.cc"

    attr_reader :fix, :shield, :ops, :models, :agents, :comply, :poll_interval

    # transport: optional callable (method, url, headers, body, timeout) -> [status, body, headers], replacing Net::HTTP.
    def initialize(api_key: nil, base_url: nil, org: nil, timeout: 30, retries: 2, backoff: 0.5, poll_interval: 3, transport: nil)
      key = (api_key || ENV["LJ_API_KEY"]).to_s.strip
      raise ArgumentError, "api_key (or LJ_API_KEY) is required" if key.empty?

      base = (base_url || ENV["LJ_BASE_URL"]).to_s.strip
      @api_key = key
      @base_url = (base.empty? ? DEFAULT_BASE_URL : base).sub(%r{/+\z}, "")
      @org = org&.to_s
      @timeout = timeout
      @retries = [retries.to_i, 0].max
      @backoff = backoff
      @poll_interval = poll_interval
      @transport = transport
      @fix = FixApi.new(self)
      @shield = ShieldApi.new(self)
      @ops = OpsApi.new(self)
      @models = ModelsApi.new(self)
      @agents = AgentsApi.new(self)
      @comply = ComplyApi.new(self)
    end

    # Calls any route. Returns {} for an empty body, {"data" => ...} for a JSON list or scalar and {"raw" => "..."} for
    # non-JSON text. Raises ApiError.
    def request(method, path, body = nil, query = nil)
      resp = send_request(method.to_s.upcase, path, body, query)
      return {} if resp.strip.empty?

      data = begin
        JSON.parse(resp)
      rescue JSON::ParserError
        return { "raw" => resp }
      end
      data.is_a?(Hash) ? data : { "data" => data }
    end

    # Raw GET body, for patches and the compliance export zip.
    def download(path, query = nil) = send_request("GET", path, nil, query)

    # Organization, scopes and key details of the API key.
    def whoami = request("GET", "/api/v1/apikeys/whoami")

    # :nodoc: used by the namespaces
    def get(path, query = nil) = request("GET", path, nil, query)
    def post(path, body = nil) = request("POST", path, body)

    # Calls the block every poll_interval seconds until it returns [row, true]; raises TimeoutError at the deadline.
    def poll_until(timeout)
      deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + (timeout.to_f.positive? ? timeout : 1800)
      loop do
        row, done = yield
        return row if done
        raise TimeoutError, "Timed out waiting." if Process.clock_gettime(Process::CLOCK_MONOTONIC) + @poll_interval > deadline

        sleep @poll_interval
      end
    end

    private

    def send_request(method, path, body, query)
      query = (query || {}).transform_keys(&:to_s).reject { |_, v| v.nil? }
      query["org"] = @org if @org && !@org.empty? && !query.key?("org")
      url = "#{@base_url}/#{path.to_s.sub(%r{\A/+}, '')}"
      url += "?#{URI.encode_www_form(query)}" unless query.empty?
      json = body.nil? ? nil : JSON.generate(body)
      headers = { "Authorization" => "Bearer #{@api_key}", "User-Agent" => "linearjeph-ruby/#{VERSION}", "Accept" => "application/json" }
      headers["Content-Type"] = "application/json" if json
      tries = method == "GET" ? 1 + @retries : 1
      last = nil
      tries.times do |attempt|
        sleep retry_wait(attempt, last) if attempt.positive?
        status, resp, rh = deliver(method, url, headers, json)
        return resp if status.between?(200, 299)

        last = build_error(status, resp, (rh["retry-after"] || rh["Retry-After"]).to_i)
        raise last unless status.zero? || status == 429 || status >= 500
      end
      raise last
    end

    def retry_wait(attempt, last)
      return [last.retry_after, 30].min if last.is_a?(ApiError) && last.retry_after.positive?

      w = @backoff * (2**(attempt - 1))
      w + (rand * w / 4)
    end

    def deliver(method, url, headers, body)
      return @transport.call(method, url, headers, body, @timeout) if @transport

      uri = URI.parse(url)
      req = Net::HTTP.const_get(method.capitalize).new(uri.request_uri, headers)
      req.body = body if body
      http = Net::HTTP.new(uri.host, uri.port)
      http.use_ssl = uri.scheme == "https"
      http.open_timeout = [@timeout, 5].min
      http.read_timeout = @timeout
      http.write_timeout = @timeout
      res = http.start { |h| h.request(req) }
      [res.code.to_i, res.body.to_s, res.each_header.to_h]
    rescue StandardError => e
      [0, "#{e.class}: #{e.message}", {}]
    end

    def build_error(status, resp, retry_after)
      return ApiError.new(0, "network_error", "Network error: #{resp}") if status.zero?

      code = "http_#{status}"
      msg = "HTTP #{status}"
      data = begin
        JSON.parse(resp)
      rescue JSON::ParserError
        nil
      end
      body = data.is_a?(Hash) ? data : {}
      if body["error"].is_a?(Hash)
        code = body["error"]["code"].to_s unless body["error"]["code"].to_s.empty?
        msg = body["error"]["message"].to_s unless body["error"]["message"].to_s.empty?
      end
      ApiError.new(status, code, msg, body, retry_after)
    end
  end
end
