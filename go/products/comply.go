package products

import (
	"context"
	"strconv"
)

const complyBase = "/api/v1/comply"

func itoa(n int) string { return strconv.Itoa(n) }

// Comply is the comply namespace: compliance frameworks, tasks, re-checks and the audit export.
type Comply struct{ c *Client }

func (m *Comply) Overview(ctx context.Context) (JSON, error) {
	return m.c.get(ctx, complyBase+"/overview", nil)
}
func (m *Comply) Compliance(ctx context.Context) (JSON, error) {
	return m.c.get(ctx, complyBase+"/compliance", nil)
}
func (m *Comply) Framework(ctx context.Context, key string) (JSON, error) {
	return m.c.get(ctx, complyBase+"/compliance/frameworks/"+key, nil)
}
func (m *Comply) EnableFramework(ctx context.Context, key string) (JSON, error) {
	return m.c.post(ctx, complyBase+"/compliance/frameworks", JSON{"key": key})
}
func (m *Comply) Tasks(ctx context.Context) (JSON, error) {
	return m.c.get(ctx, complyBase+"/compliance/tasks", nil)
}
func (m *Comply) Recheck(ctx context.Context) (JSON, error) {
	return m.c.post(ctx, complyBase+"/compliance/recheck", nil)
}

// Export downloads the audit package (a zip).
func (m *Comply) Export(ctx context.Context) ([]byte, error) {
	return m.c.Download(ctx, complyBase+"/compliance/export", nil)
}
