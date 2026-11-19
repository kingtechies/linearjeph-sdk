# frozen_string_literal: true

require "json"
require "digest"
require "time"

module Linearjeph
  module Prophecy
    DEFAULT_ENDPOINT = "https://linearjeph.cc"
    KINDS = %w[signup login active feature payment refund subscription churn pageview request error custom].freeze

    # Prophecy client. Tracking calls enqueue and return immediately; a background thread sends batches every
    # flush_interval seconds or batch_size events, and at_exit flushes. Tracking never raises. Secret-key calls
    # (report, refresh, live, embed_url, import_history, send_manifest) raise Linearjeph::Prophecy::Error.
    class Client
      attr_reader :dropped

      def initialize(ingest_key: nil, secret_key: nil, endpoint: DEFAULT_ENDPOINT, hash_user_ids: true,
                     queue_size: 10_000, batch_size: 100, flush_interval: 2.0, retries: 4, backoff: 0.5,
                     timeout: 10, debug: false, start_thread: true)
        @ingest_key = ingest_key.to_s.strip
        @secret_key = secret_key.to_s.strip
        raise ArgumentError, "Prophecy: ingest_key (or secret_key) is required" if @ingest_key.empty? && @secret_key.empty?

        @endpoint = (endpoint || DEFAULT_ENDPOINT).to_s.chomp("/")
        @hash = hash_user_ids != false
        @queue_size = [queue_size.to_i, 1].max
        @batch_size = [[batch_size.to_i, 1].max, 500].min
        @flush_interval = flush_interval.to_f
        @retries = [retries.to_i, 0].max
        @backoff = backoff.to_f
        @timeout = timeout.to_f
        @debug = debug
        @start_thread = start_thread
        @events = []
        @customers = []
        @dropped = 0
        @closed = false
        @warned = {}
        @mutex = Mutex.new
        @cond = ConditionVariable.new
        @flush_mutex = Mutex.new
        @thread = nil
        @pid = Process.pid
      end

      def self.hash_uid(uid) = Digest::SHA256.hexdigest(uid.to_s)

      # ---- tracking (never raise) -------------------------------------------------------------------------

      def track(name, uid: nil, kind: nil, feature: nil, amount: nil, currency: nil, props: nil, ts: nil)
        safe do
          name = name.to_s.strip
          next if name.empty?

          kind = kind.to_s
          kind = feature && !feature.to_s.empty? ? "feature" : "custom" unless KINDS.include?(kind)
          ev = { "ts" => iso(ts), "name" => name[0, 80], "kind" => kind }
          u = uid_of(uid)
          ev["uid"] = u unless u.empty?
          ev["feature"] = feature.to_s[0, 80] if feature && !feature.to_s.empty?
          ev["amount"] = amount if amount.is_a?(Numeric) && amount.to_f.finite?
          ev["currency"] = currency.to_s[0, 3].upcase if currency && !currency.to_s.empty?
          p = clean_props(props)
          ev["props"] = p if p
          enqueue_event(ev)
        end
      end

      def feature(name, uid = nil)
        track(name, kind: "feature", feature: name, uid: uid)
      end

      def signup(uid, plan: nil, channel: nil, country: nil, ts: nil)
        safe do
          p = strip("plan" => plan, "channel" => channel, "country" => country)
          track("signup", kind: "signup", uid: uid, ts: ts, props: p)
          identify(uid, p.merge("signup_at" => iso(ts)))
        end
      end

      # A negative amount is recorded as a refund.
      def payment(uid, amount, currency = nil, plan: nil, ts: nil)
        kind = amount.to_f.negative? ? "refund" : "payment"
        track(kind, kind: kind, uid: uid, amount: amount, currency: currency || "USD", ts: ts, props: strip("plan" => plan))
      end

      def subscription(uid, plan: nil, mrr: nil, status: "active", ts: nil)
        safe do
          p = strip("plan" => plan, "mrr" => mrr, "status" => status)
          track("subscription", kind: "subscription", uid: uid, ts: ts, props: p)
          identify(uid, strip("plan" => plan, "mrr" => mrr))
        end
      end

      def churn(uid, reason = nil)
        track("churn", kind: "churn", uid: uid, props: reason ? { "reason" => reason.to_s[0, 200] } : nil)
      end

      # plan, mrr, country, channel and signup_at become customer fields; other keys (max 20) are traits.
      def identify(uid, traits = {})
        safe do
          u = uid_of(uid)
          next if u.empty?

          c = { "uid" => u }
          rest = {}
          (traits || {}).each do |k, v|
            k = k.to_s
            k = "signup_at" if k == "signupAt"
            if %w[plan mrr country channel signup_at].include?(k)
              c[k] = v unless v.nil? || v == ""
            elsif rest.size < 20
              rest[k] = v
            end
          end
          rest = clean_props(rest)
          c["traits"] = rest if rest
          enqueue_customer(c)
        end
      end

      # Record one handled request: route template, method, HTTP status, duration in ms. No bodies, no query strings.
      def record_request(route, method, status, duration_ms)
        track("request", kind: "request", props: {
                "route" => route.to_s[0, 200], "method" => method.to_s.upcase[0, 10],
                "status" => status.to_i, "duration_ms" => duration_ms.to_f.round(1)
              })
      end

      # ---- delivery -------------------------------------------------------------------------------------

      # Send everything queued. Returns true when the queue is empty afterwards. Never raises.
      def flush
        @flush_mutex.synchronize do
          loop do
            ev, cu = take
            break if ev.empty? && cu.empty?

            deliver("/ingest/v1/prophecy/identify", { "customers" => cu }) unless cu.empty?
            deliver("/ingest/v1/prophecy/events", { "events" => ev }) unless ev.empty?
          end
        end
        @mutex.synchronize { @events.empty? && @customers.empty? }
      rescue StandardError => e
        warn_once("flush", "flush error: #{e.message}")
        false
      end

      def close
        ok = flush
        @mutex.synchronize do
          @closed = true
          @cond.broadcast
        end
        @thread&.join(2) if @thread && @thread != Thread.current
        ok
      end

      # ---- manifest + secret-key API (raise Prophecy::Error) ----------------------------------------------

      def send_manifest(manifest)
        raise Error, "ingest_key is required to send a manifest" if @ingest_key.empty?

        request("POST", "/ingest/v1/prophecy/manifest", { "manifest" => manifest }, false)
        true
      end

      def report(section = nil)
        secret("GET", "/api/v1/prophecy/sdk/report#{section ? "?section=#{URI.encode_www_form_component(section)}" : ''}")
      end

      def refresh = secret("POST", "/api/v1/prophecy/sdk/refresh", {})
      def live = secret("GET", "/api/v1/prophecy/sdk/live")

      def embed_url(ttl = 3600)
        r = secret("POST", "/api/v1/prophecy/sdk/embed", { "ttl" => [[ttl.to_i, 60].max, 86_400].min })
        r["url"] || raise(Error, "no url in response")
      end

      # Backfill history in batches of 5,000 rows / 5 MB with retries. kind: "events" or "customers". uids are hashed like
      # live events. A block receives (done, total) after each batch.
      def import_history(kind, rows, &progress)
        kind = kind.to_s
        raise Error, 'kind must be "events" or "customers"' unless %w[events customers].include?(kind)
        raise Error, "secret_key is required for import_history" if @secret_key.empty?

        all = rows.map do |r|
          r = r.transform_keys(&:to_s)
          r["uid"] = uid_of(r["uid"]) if r["uid"] && r["uid"].to_s != ""
          r
        end
        total = all.size
        res = { "accepted" => 0, "rejected" => 0, "errors" => [], "jobs" => [] }
        done = 0
        post = lambda do |batch|
          if batch.size > 1 && JSON.generate(batch).bytesize > 4500 * 1024
            h = batch.size / 2
            post.call(batch[0, h])
            post.call(batch[h..])
            next
          end
          out = secret("POST", "/api/v1/prophecy/sdk/import", { "kind" => kind, "rows" => batch })
          res["accepted"] += out["accepted"].to_i
          res["rejected"] += out["rejected"].to_i
          res["jobs"] << out["job"] if out.key?("job")
          Array(out["errors"]).each { |e| res["errors"] << e.to_s if res["errors"].size < 20 }
          done += batch.size
          begin
            progress&.call(done, total)
          rescue StandardError => e
            warn_once("progress", "progress callback failed: #{e.message}")
          end
        end
        all.each_slice(5000) { |b| post.call(b) }
        res
      end

      private

      def secret(method, path, body = nil)
        raise Error, "secret_key is required for this call (server side only)" if @secret_key.empty?

        _, resp = request(method, path, body, true)
        data = JSON.parse(resp.to_s.empty? ? "{}" : resp)
        data.is_a?(Hash) ? data : {}
      rescue JSON::ParserError
        {}
      end

      # Retries network errors, 429 and 5xx with exponential backoff; raises Error otherwise.
      def request(method, path, body, secret)
        json = body.nil? ? nil : JSON.generate(body)
        headers = { "User-Agent" => "linearjeph-prophecy-ruby/#{VERSION}", "Accept" => "application/json" }
        if secret
          headers["Authorization"] = "Bearer #{@secret_key}"
        else
          headers["X-LJ-Key"] = @ingest_key
        end
        last = nil
        (@retries + 1).times do |attempt|
          if attempt.positive?
            wait = @backoff * (2**(attempt - 1))
            wait += rand * wait / 2
            wait = last.retry_after if last.is_a?(Error) && last.retry_after.positive? && last.retry_after <= 30
            sleep(wait)
          end
          status, resp, rh = Http.send_request(method, @endpoint + path, headers, json, @timeout)
          if status.zero?
            last = Error.new("network error: #{resp}")
            next
          end
          if status == 429 || status >= 500
            last = Error.new("HTTP #{status} #{resp[0, 200]}", status, rh["retry-after"].to_i)
            next
          end
          raise Error.new("HTTP #{status} #{resp[0, 200]}", status) if status >= 400

          return [status, resp]
        end
        raise last || Error.new("request failed")
      end

      def deliver(path, body)
        request("POST", path, body, false)
      rescue StandardError => e
        warn_once("deliver", "delivery to #{path} failed: #{e.message}")
      end

      def take
        @mutex.synchronize do
          [@events.shift(@batch_size), @customers.shift(@batch_size)]
        end
      end

      def enqueue_event(ev)
        wake = false
        @mutex.synchronize do
          if @closed || @ingest_key.empty? || @events.size >= @queue_size
            @dropped += 1
            next
          end
          @events << ev
          wake = @events.size >= @batch_size
        end
        ensure_thread
        @mutex.synchronize { @cond.signal } if wake
      end

      def enqueue_customer(c)
        @mutex.synchronize do
          if @closed || @ingest_key.empty? || @customers.size >= @queue_size
            @dropped += 1
            next
          end
          @customers << c
        end
        ensure_thread
      end

      # Starts (or, after a fork, restarts) the background sender.
      def ensure_thread
        return unless @start_thread
        return if @thread&.alive? && @pid == Process.pid

        @mutex.synchronize do
          return if @thread&.alive? && @pid == Process.pid

          @pid = Process.pid
          @thread = Thread.new { run }
          @thread.report_on_exception = false
        end
      end

      def run
        loop do
          @mutex.synchronize do
            @cond.wait(@mutex, @flush_interval) if @events.size < @batch_size && !@closed
          end
          flush
          break if @closed
        end
      rescue StandardError => e
        warn_once("thread", "sender stopped: #{e.message}")
      end

      def uid_of(uid)
        return "" if uid.nil? || uid.to_s.empty?

        @hash ? Client.hash_uid(uid) : uid.to_s[0, 200]
      end

      def iso(ts)
        t = case ts
            when Time then ts
            when Numeric then Time.at(ts > 1e11 ? ts / 1000.0 : ts)
            when String then ts.empty? ? Time.now : parse_time(ts)
            else Time.now
            end
        t.utc.strftime("%Y-%m-%dT%H:%M:%S.%LZ")
      end

      def parse_time(str)
        Time.parse(str)
      rescue ArgumentError
        Time.now
      end

      def clean_props(props)
        return nil unless props.is_a?(Hash) && !props.empty?

        s = JSON.generate(props)
        if s.bytesize > 2048
          warn_once("props", "props dropped (over 2 KB)")
          return nil
        end
        props
      rescue StandardError
        warn_once("props", "props dropped (not serialisable)")
        nil
      end

      def strip(hash) = hash.reject { |_, v| v.nil? || v == "" }

      def safe
        yield
      rescue StandardError => e
        warn_once("internal", "ignored internal error: #{e.class}: #{e.message}")
        nil
      end

      def warn_once(key, msg)
        return if @warned[key] && !@debug

        @warned[key] = true
        Kernel.warn("[prophecy] #{msg}")
      end
    end
  end
end
