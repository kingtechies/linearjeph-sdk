# frozen_string_literal: true

require "net/http"
require "uri"
require "zlib"
require "stringio"

module Linearjeph
  module Prophecy
    # Minimal Net::HTTP transport. Returns [status, body, headers]; status 0 means a network error (body = message).
    module Http
      module_function

      def send_request(method, url, headers, body, timeout)
        uri = URI.parse(url)
        headers = headers.dup
        payload = body
        if payload
          payload = Zlib.gzip(payload)
          headers["Content-Encoding"] = "gzip"
          headers["Content-Type"] = "application/json"
        end
        klass = method == "GET" ? Net::HTTP::Get : Net::HTTP::Post
        req = klass.new(uri.request_uri, headers)
        req.body = payload if payload
        http = Net::HTTP.new(uri.host, uri.port)
        http.use_ssl = uri.scheme == "https"
        http.open_timeout = [timeout, 5].min
        http.read_timeout = timeout
        http.write_timeout = timeout
        res = http.start { |h| h.request(req) }
        [res.code.to_i, res.body.to_s, res.each_header.to_h]
      rescue StandardError => e
        [0, "#{e.class}: #{e.message}", {}]
      end
    end
  end
end
