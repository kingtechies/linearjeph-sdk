package products

import "context"

const agentsBase = "/api/v1/agentcloud"

// Agents is the agents namespace (Agent Cloud).
type Agents struct{ c *Client }

func (a *Agents) Overview(ctx context.Context) (JSON, error) {
	return a.c.get(ctx, agentsBase+"/overview", nil)
}
func (a *Agents) List(ctx context.Context) (JSON, error) {
	return a.c.get(ctx, agentsBase+"/agents", nil)
}
func (a *Agents) Get(ctx context.Context, id int) (JSON, error) {
	return a.c.get(ctx, idPath(agentsBase+"/agents", id), nil)
}
func (a *Agents) Runs(ctx context.Context, id int) (JSON, error) {
	return a.c.get(ctx, idPath(agentsBase+"/agents", id, "runs"), nil)
}
func (a *Agents) Run(ctx context.Context, id, runID int) (JSON, error) {
	return a.c.get(ctx, idPath(agentsBase+"/agents", id, "runs/"+itoa(runID)), nil)
}

// Action runs pause, resume, redeploy, run or rotate-webhook. input is only used by "run".
func (a *Agents) Action(ctx context.Context, id int, action string, input any) (JSON, error) {
	var body any
	if input != nil {
		body = JSON{"input": input}
	}
	return a.c.post(ctx, idPath(agentsBase+"/agents", id, action), body)
}
