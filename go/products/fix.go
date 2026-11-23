package products

import (
	"context"
	"strconv"
	"time"
)

const fixBase = "/api/v1/fixops"

// Fix is the fixops namespace: AI fix jobs, projects and migrations.
type Fix struct{ c *Client }

// FinalJobStatuses end a wait. "pr_open" is included: the work is done and only a person's decision is left.
var FinalJobStatuses = map[string]bool{"done": true, "accepted": true, "failed": true, "rejected": true, "cancelled": true, "pr_open": true}

// FailedJobStatuses are the final statuses that mean the job did not produce a fix.
var FailedJobStatuses = map[string]bool{"failed": true, "rejected": true, "cancelled": true}

func (f *Fix) Overview(ctx context.Context) (JSON, error) {
	return f.c.get(ctx, fixBase+"/overview", nil)
}

// Jobs lists jobs. Filters: product, kind, status, project.
func (f *Fix) Jobs(ctx context.Context, q Query) (JSON, error) {
	return f.c.get(ctx, fixBase+"/jobs", q)
}

// CreateJob starts a fix job. Body keys: source_id, issue, title, logs, repro_command, test_command, url, run_on, server_ref.
func (f *Fix) CreateJob(ctx context.Context, body JSON) (JSON, error) {
	return f.c.post(ctx, fixBase+"/jobs", body)
}
func (f *Fix) Job(ctx context.Context, id int) (JSON, error) {
	return f.c.get(ctx, idPath(fixBase+"/jobs", id), nil)
}

// JobEvents returns events after the given event id.
func (f *Fix) JobEvents(ctx context.Context, id, after int) (JSON, error) {
	return f.c.get(ctx, idPath(fixBase+"/jobs", id, "events"), Query{"after": strconv.Itoa(after)})
}
func (f *Fix) CancelJob(ctx context.Context, id int) (JSON, error) {
	return f.c.post(ctx, idPath(fixBase+"/jobs", id, "cancel"), nil)
}
func (f *Fix) AcceptJob(ctx context.Context, id int) (JSON, error) {
	return f.c.post(ctx, idPath(fixBase+"/jobs", id, "accept"), nil)
}
func (f *Fix) RejectJob(ctx context.Context, id int, reason string) (JSON, error) {
	return f.c.post(ctx, idPath(fixBase+"/jobs", id, "reject"), JSON{"reason": reason})
}
func (f *Fix) RollbackJob(ctx context.Context, id int) (JSON, error) {
	return f.c.post(ctx, idPath(fixBase+"/jobs", id, "rollback"), nil)
}

// JobPatch downloads the unified diff of a job.
func (f *Fix) JobPatch(ctx context.Context, id int) (string, error) {
	b, err := f.c.Download(ctx, idPath(fixBase+"/jobs", id, "patch"), nil)
	return string(b), err
}

func (f *Fix) Projects(ctx context.Context) (JSON, error) {
	return f.c.get(ctx, fixBase+"/projects", nil)
}

// CreateProject enrols a source. Body keys: source_id, name, review_enabled, deps_enabled, tests_enabled, ...
func (f *Fix) CreateProject(ctx context.Context, body JSON) (JSON, error) {
	return f.c.post(ctx, fixBase+"/projects", body)
}
func (f *Fix) Project(ctx context.Context, id int) (JSON, error) {
	return f.c.get(ctx, idPath(fixBase+"/projects", id), nil)
}
func (f *Fix) UpdateProject(ctx context.Context, id int, body JSON) (JSON, error) {
	return f.c.Request(ctx, "PATCH", idPath(fixBase+"/projects", id), body, nil)
}

// Review runs the code review poll now.
func (f *Fix) Review(ctx context.Context, project int) (JSON, error) {
	return f.c.post(ctx, idPath(fixBase+"/projects", project, "review-now"), nil)
}

// Deps queues a dependency update job.
func (f *Fix) Deps(ctx context.Context, project int) (JSON, error) {
	return f.c.post(ctx, idPath(fixBase+"/projects", project, "deps-now"), nil)
}

// Tests queues a test writing job (maxFiles <= 0 keeps the server default of 5).
func (f *Fix) Tests(ctx context.Context, project, maxFiles int) (JSON, error) {
	var body JSON
	if maxFiles > 0 {
		body = JSON{"max_files": maxFiles}
	}
	return f.c.post(ctx, idPath(fixBase+"/projects", project, "tests-now"), body)
}

func (f *Fix) Migrations(ctx context.Context) (JSON, error) {
	return f.c.get(ctx, fixBase+"/migrations", nil)
}
func (f *Fix) Migration(ctx context.Context, id int) (JSON, error) {
	return f.c.get(ctx, idPath(fixBase+"/migrations", id), nil)
}

// MigrationPlan generates the migration plan.
func (f *Fix) MigrationPlan(ctx context.Context, id int) (JSON, error) {
	return f.c.post(ctx, idPath(fixBase+"/migrations", id, "plan"), nil)
}

// Wait polls a job until it reaches a final status (see FinalJobStatuses) and returns it. A zero timeout means 30
// minutes. It returns ErrTimeout (with the last job) when the deadline passes first.
func (f *Fix) Wait(ctx context.Context, id int, timeout time.Duration) (JSON, error) {
	return f.c.pollUntil(ctx, timeout, func() (JSON, bool, error) {
		j, err := f.Job(ctx, id)
		if err != nil {
			return nil, false, err
		}
		return j, FinalJobStatuses[Str(j, "status")], nil
	})
}
