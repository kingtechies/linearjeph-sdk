# frozen_string_literal: true

require "linearjeph/prophecy"
require "linearjeph/client"
require "linearjeph/fix_api"
require "linearjeph/shield_api"
require "linearjeph/ops_api"
require "linearjeph/models_api"
require "linearjeph/agents_api"
require "linearjeph/comply_api"

# LINEARJEPH SDK. Prophecy lives in Linearjeph::Prophecy, the product API client in Linearjeph::Client.
module Linearjeph
end

# Alias so both spellings work: LinearJeph::Client and Linearjeph::Client.
LinearJeph = Linearjeph
