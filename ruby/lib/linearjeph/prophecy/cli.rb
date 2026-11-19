# frozen_string_literal: true

require "json"

module Linearjeph
  module Prophecy
    # prophecy scan [dir] [--send]
    # prophecy import <events|customers> <file.csv|file.json>
    # Keys: LINEARJEPH_INGEST_KEY, LINEARJEPH_SECRET_KEY, LINEARJEPH_ENDPOINT.
    module Cli
      TOP = %w[ts uid name kind feature amount currency id signup_at plan mrr country channel].freeze

      module_function

      def run(args, out = $stdout, err = $stderr)
        case args.first
        when "scan"
          rest = args[1..]
          send = rest.delete("--send")
          dir = rest.first || "."
          begin
            m = Scanner.new.scan(dir)
            if send
              client.send_manifest(m)
              out.puts format("manifest sent: %d files in %d languages, %d routes, %d dependencies",
                              m["languages"].values.sum, m["languages"].size, m["routes"].size, m["dependencies"].size)
            else
              out.puts JSON.pretty_generate(m)
            end
            0
          rescue StandardError => e
            err.puts "#{send ? 'send' : 'scan'} failed: #{e.message}"
            1
          end
        when "import"
          return usage(err) unless args.size == 3

          begin
            rows = load_rows(args[2], args[1])
            res = client.import_history(args[1], rows) { |d, t| err.puts "imported #{d}/#{t}" }
            out.puts "accepted #{res['accepted']}, rejected #{res['rejected']}"
            res["errors"].each { |e| err.puts "  #{e}" }
            0
          rescue StandardError => e
            err.puts "import failed: #{e.message}"
            1
          end
        else
          usage(err)
        end
      end

      def usage(err)
        err.puts "usage: prophecy scan [dir] [--send]\n       prophecy import <events|customers> <file.csv|file.json>"
        2
      end

      def client
        Client.new(ingest_key: ENV["LINEARJEPH_INGEST_KEY"], secret_key: ENV["LINEARJEPH_SECRET_KEY"],
                   endpoint: ENV["LINEARJEPH_ENDPOINT"] || DEFAULT_ENDPOINT, start_thread: false)
      end

      def load_rows(path, kind)
        raise Error, "file not found: #{path}" unless File.file?(path)

        if path.downcase.end_with?(".json")
          raw = JSON.parse(File.read(path))
          list = raw.is_a?(Array) ? raw : raw["rows"]
          raise Error, 'expected a JSON array of objects (or {"rows": [...]})' unless list.is_a?(Array)

          return list.select { |r| r.is_a?(Hash) }
        end
        require "csv"
        CSV.read(path, headers: true).map do |rec|
          row = {}
          props = {}
          rec.to_h.each do |h, v|
            h = h.to_s.strip
            next if v.nil? || v == ""

            if %w[amount mrr].include?(h)
              row[h] = Float(v, exception: false) || next
            elsif h.start_with?("props.")
              props[h.delete_prefix("props.")] = v
            elsif TOP.include?(h)
              row[h] = v
            else
              props[h] = v
            end
          end
          row[kind == "customers" ? "traits" : "props"] = props unless props.empty?
          row
        end
      end
    end
  end
end
