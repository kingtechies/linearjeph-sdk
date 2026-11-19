# frozen_string_literal: true

require "linearjeph/prophecy/version"
require "linearjeph/prophecy/http"
require "linearjeph/prophecy/client"
require "linearjeph/prophecy/scanner"
require "linearjeph/prophecy/rack"
require "linearjeph/prophecy/cli"

module Linearjeph
  # Prophecy growth intelligence.
  #
  #   Linearjeph::Prophecy.init(ingest_key: ENV["LINEARJEPH_INGEST_KEY"], secret_key: ENV["LINEARJEPH_SECRET_KEY"])
  #   Linearjeph::Prophecy.signup(user.id, plan: "free", channel: "ads")
  #   Linearjeph::Prophecy.payment(user.id, 49.0, "USD", plan: "pro")
  #
  # Tracking calls never raise into the host app and are no-ops before init.
  module Prophecy
    class Error < StandardError
      attr_reader :status

      def initialize(message = nil, status = 0, retry_after = 0)
        super(message)
        @status = status
        @retry_after = retry_after
      end

      attr_reader :retry_after
    end

    class << self
      attr_reader :client

      def init(**options)
        old = @client
        @client = Client.new(**options)
        old&.close
        install_at_exit
        @client
      end

      # Replace the client (tests).
      def client=(c)
        @client = c
      end

      def track(name, **o) = @client&.track(name, **o)
      def feature(name, uid = nil) = @client&.feature(name, uid)
      def signup(uid, **o) = @client&.signup(uid, **o)
      def payment(uid, amount, currency = nil, **o) = @client&.payment(uid, amount, currency, **o)
      def subscription(uid, **o) = @client&.subscription(uid, **o)
      def churn(uid, reason = nil) = @client&.churn(uid, reason)
      def identify(uid, traits = {}) = @client&.identify(uid, traits)
      def record_request(route, method, status, duration_ms) = @client&.record_request(route, method, status, duration_ms)

      # Rack middleware class: `use Linearjeph::Prophecy.middleware` (Rails: config.middleware.use ...).
      def middleware = Rack

      def scan(root = ".") = Scanner.new.scan(root)

      def send_manifest(manifest) = need.send_manifest(manifest)
      def import_history(kind, rows, &progress) = need.import_history(kind, rows, &progress)
      def report(section = nil) = need.report(section)
      def refresh = need.refresh
      def live = need.live
      def embed_url(ttl = 3600) = need.embed_url(ttl)
      def flush = @client ? @client.flush : true
      def close = @client ? @client.close : true

      private

      def need
        @client || raise(Error, "call Linearjeph::Prophecy.init first")
      end

      def install_at_exit
        return if @at_exit

        @at_exit = true
        at_exit { Linearjeph::Prophecy.close rescue nil } # rubocop:disable Style/RescueModifier
      end
    end
  end
end
