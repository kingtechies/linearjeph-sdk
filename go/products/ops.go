package products

import "context"

const opsBase = "/api/v1/opsagent"
const backupsBase = "/api/v1/backups/agent"

// Ops is the ops namespace: on-call summary, servers, incidents, cost, managed databases and backup targets.
type Ops struct{ c *Client }

func (o *Ops) Summary(ctx context.Context) (JSON, error) {
	return o.c.get(ctx, opsBase+"/summary", nil)
}
func (o *Ops) Servers(ctx context.Context) (JSON, error) {
	return o.c.get(ctx, opsBase+"/servers", nil)
}

// Incidents lists incidents; status may be empty, "open" or "resolved".
func (o *Ops) Incidents(ctx context.Context, status string) (JSON, error) {
	q := Query{}
	if status != "" {
		q["status"] = status
	}
	return o.c.get(ctx, opsBase+"/incidents", q)
}
func (o *Ops) Incident(ctx context.Context, id int) (JSON, error) {
	return o.c.get(ctx, idPath(opsBase+"/incidents", id), nil)
}
func (o *Ops) ResolveIncident(ctx context.Context, id int) (JSON, error) {
	return o.c.post(ctx, idPath(opsBase+"/incidents", id, "resolve"), nil)
}
func (o *Ops) CostSummary(ctx context.Context) (JSON, error) {
	return o.c.get(ctx, opsBase+"/cost", nil)
}
func (o *Ops) CostAnalyse(ctx context.Context) (JSON, error) {
	return o.c.post(ctx, opsBase+"/cost/analyse", nil)
}
func (o *Ops) ManagedDatabases(ctx context.Context) (JSON, error) {
	return o.c.get(ctx, opsBase+"/managed-db", nil)
}
func (o *Ops) BackupTargets(ctx context.Context) (JSON, error) {
	return o.c.get(ctx, backupsBase+"/targets", nil)
}
func (o *Ops) RunBackup(ctx context.Context, target int) (JSON, error) {
	return o.c.post(ctx, idPath(backupsBase+"/targets", target, "run"), nil)
}
func (o *Ops) RunDrill(ctx context.Context, target int) (JSON, error) {
	return o.c.post(ctx, idPath(backupsBase+"/targets", target, "drill"), nil)
}
func (o *Ops) BackupRuns(ctx context.Context, target int) (JSON, error) {
	return o.c.get(ctx, idPath(backupsBase+"/targets", target, "runs"), nil)
}
