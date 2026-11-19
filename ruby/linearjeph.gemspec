# frozen_string_literal: true

require_relative "lib/linearjeph/prophecy/version"

Gem::Specification.new do |s|
  s.name        = "linearjeph"
  s.version     = Linearjeph::Prophecy::VERSION
  s.summary     = "Revenue forecasting and growth analytics for Ruby and Rails apps (LINEARJEPH Prophecy)"
  s.description = "Track signups, payments, subscriptions, features and API requests (Rack/Rails middleware), scan your " \
                  "codebase locally into a manifest, import history and get a 12-month MRR/ARR forecast with ranges, churn, " \
                  "cohort retention, feature impact and a ranked 90-day plan. Standard library only."
  s.authors     = ["LINEARJEPH"]
  s.email       = ["support@linearjeph.cc"]
  s.homepage    = "https://linearjeph.cc/prophecy"
  s.license     = "MIT"
  s.required_ruby_version = ">= 3.0"
  s.metadata    = {
    "homepage_uri" => "https://linearjeph.cc/prophecy",
    "documentation_uri" => "https://linearjeph.cc/docs/sdk/prophecy",
    "source_code_uri" => "https://github.com/kingtechies/linearjeph-sdk/tree/main/ruby",
    "changelog_uri" => "https://github.com/kingtechies/linearjeph-sdk/blob/main/ruby/CHANGELOG.md",
    "rubygems_mfa_required" => "true",
  }
  s.files       = Dir["lib/**/*.rb", "exe/*", "LICENSE", "README.md", "CHANGELOG.md"]
  s.bindir      = "exe"
  s.executables = ["prophecy"]
  s.require_paths = ["lib"]
end
