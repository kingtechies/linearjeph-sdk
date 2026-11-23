package products

import "context"

const endpointsBase = "/api/v1/endpoints"
const privateBase = "/api/v1/agentcloud/private/endpoints"
const finetuneBase = "/api/v1/agentcloud/finetune"

// Models is the models namespace: AI endpoints, private endpoints and fine-tuning.
type Models struct{ c *Client }

func (m *Models) Endpoints(ctx context.Context) (JSON, error) {
	return m.c.get(ctx, endpointsBase+"/endpoints", nil)
}
func (m *Models) Endpoint(ctx context.Context, id int) (JSON, error) {
	return m.c.get(ctx, idPath(endpointsBase+"/endpoints", id), nil)
}
func (m *Models) EndpointUsage(ctx context.Context, id int) (JSON, error) {
	return m.c.get(ctx, idPath(endpointsBase+"/endpoints", id, "usage"), nil)
}

// EndpointAction runs suspend, resume or rotate-key.
func (m *Models) EndpointAction(ctx context.Context, id int, action string) (JSON, error) {
	return m.c.post(ctx, idPath(endpointsBase+"/endpoints", id, action), nil)
}
func (m *Models) PrivateEndpoints(ctx context.Context) (JSON, error) {
	return m.c.get(ctx, privateBase, nil)
}
func (m *Models) PrivateEndpoint(ctx context.Context, id int) (JSON, error) {
	return m.c.get(ctx, idPath(privateBase, id), nil)
}
func (m *Models) PrivateUsage(ctx context.Context, id int) (JSON, error) {
	return m.c.get(ctx, idPath(privateBase, id, "usage"), nil)
}
func (m *Models) PrivateLogs(ctx context.Context, id int) (JSON, error) {
	return m.c.get(ctx, idPath(privateBase, id, "logs"), nil)
}
func (m *Models) FinetuneDatasets(ctx context.Context) (JSON, error) {
	return m.c.get(ctx, finetuneBase+"/datasets", nil)
}
func (m *Models) FinetuneJobs(ctx context.Context) (JSON, error) {
	return m.c.get(ctx, finetuneBase+"/jobs", nil)
}
func (m *Models) FinetuneJob(ctx context.Context, id int) (JSON, error) {
	return m.c.get(ctx, idPath(finetuneBase+"/jobs", id), nil)
}

// CreateFinetuneJob body keys: dataset_id, base_model, method.
func (m *Models) CreateFinetuneJob(ctx context.Context, body JSON) (JSON, error) {
	return m.c.post(ctx, finetuneBase+"/jobs", body)
}
