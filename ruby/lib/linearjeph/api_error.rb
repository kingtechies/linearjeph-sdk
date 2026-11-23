# frozen_string_literal: true

module Linearjeph
  # Every non-2xx answer, built from {"error": {"code", "message"}}. status 0 means a network error.
  class ApiError < StandardError
    attr_reader :status, :code, :body, :retry_after

    def initialize(status, code, message, body = {}, retry_after = 0)
      super(message)
      @status = status
      @code = code
      @body = body
      @retry_after = retry_after
    end
  end

  # Raised by the wait helpers when the deadline passes first.
  class TimeoutError < StandardError; end
end
