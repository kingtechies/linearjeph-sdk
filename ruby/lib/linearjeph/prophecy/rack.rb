# frozen_string_literal: true

module Linearjeph
  module Prophecy
    # Rack middleware: records route template, method, status and duration (no bodies, no query strings).
    #   Rails:   config.middleware.use Linearjeph::Prophecy::Rack
    #   Sinatra: use Linearjeph::Prophecy::Rack
    # Route template: Rails env["action_dispatch.route_uri_pattern"], Sinatra env["sinatra.route"], else the path with
    # numeric, UUID and long hex segments normalised to :id.
    class Rack
      UUID = /\A\h{8}-\h{4}-\h{4}-\h{4}-\h{12}\z/.freeze

      def self.normalize(path)
        path = path.to_s.split(/[?#]/, 2).first.to_s
        return "/" if path.empty?

        path.split("/", -1).map { |s| !s.empty? && (s.match?(/\A\d+\z/) || s.match?(UUID) || s.match?(/\A\h{16,}\z/)) ? ":id" : s }.join("/")
      end

      def self.route_of(env)
        if (r = env["action_dispatch.route_uri_pattern"]) && !r.to_s.empty?
          r.to_s.sub(/\(\.:format\)\z/, "")
        elsif (s = env["sinatra.route"]) && !s.to_s.empty?
          s.to_s.sub(/\A[A-Z]+ /, "")
        else
          normalize(env["PATH_INFO"])
        end
      end

      def initialize(app)
        @app = app
      end

      def call(env)
        start = Process.clock_gettime(Process::CLOCK_MONOTONIC)
        begin
          status, headers, body = @app.call(env)
        rescue Exception # rubocop:disable Lint/RescueException
          record(env, 500, start)
          raise
        end
        record(env, status, start)
        [status, headers, body]
      end

      private

      def record(env, status, start)
        ms = (Process.clock_gettime(Process::CLOCK_MONOTONIC) - start) * 1000
        Prophecy.record_request(Rack.route_of(env), env["REQUEST_METHOD"], status.to_i, ms)
      rescue StandardError
        nil
      end
    end
  end
end
