# frozen_string_literal: true

module Linearjeph
  # models: AI endpoints, private endpoints and fine-tuning.
  class ModelsApi
    ENDPOINTS = "/api/v1/endpoints/endpoints"
    PRIVATE = "/api/v1/agentcloud/private/endpoints"
    FINETUNE = "/api/v1/agentcloud/finetune"

    def initialize(client) = @c = client

    def endpoints = @c.get(ENDPOINTS)
    def endpoint(id) = @c.get("#{ENDPOINTS}/#{id}")
    def endpoint_usage(id) = @c.get("#{ENDPOINTS}/#{id}/usage")
    # action: suspend, resume or rotate-key
    def endpoint_action(id, action) = @c.post("#{ENDPOINTS}/#{id}/#{action}")
    def private_endpoints = @c.get(PRIVATE)
    def private_endpoint(id) = @c.get("#{PRIVATE}/#{id}")
    def private_usage(id) = @c.get("#{PRIVATE}/#{id}/usage")
    def private_logs(id) = @c.get("#{PRIVATE}/#{id}/logs")
    def finetune_datasets = @c.get("#{FINETUNE}/datasets")
    def finetune_jobs = @c.get("#{FINETUNE}/jobs")
    def finetune_job(id) = @c.get("#{FINETUNE}/jobs/#{id}")
    # body keys: dataset_id, base_model, method
    def create_finetune_job(body = nil, **kw) = @c.post("#{FINETUNE}/jobs", body || kw)
  end
end
