package products

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const shieldBase = "/api/v1/shield"
const securityBase = "/api/v1/security"

// Shield is the shield namespace: vibe scans, secret watch, pentest, WAF, phishing and the site security scans.
type Shield struct{ c *Client }

var severityRank = map[string]int{"info": 0, "low": 1, "medium": 2, "high": 3, "critical": 4}

// SeverityRank orders severities (info 0 ... critical 4). Unknown severities rank -1.
func SeverityRank(s string) int {
	if r, ok := severityRank[strings.ToLower(s)]; ok {
		return r
	}
	return -1
}

// Gate returns the findings of a scan detail at or above failOn (default "high"). An unknown failOn is an error.
func (s *Shield) Gate(scan JSON, failOn string) ([]JSON, error) {
	if failOn == "" {
		failOn = "high"
	}
	min, ok := severityRank[strings.ToLower(failOn)]
	if !ok {
		return nil, fmt.Errorf("linearjeph: fail-on must be one of low, medium, high, critical (got %q)", failOn)
	}
	arr, _ := scan["findings"].([]any)
	var out []JSON
	for _, x := range arr {
		f, ok := x.(map[string]any)
		if ok && SeverityRank(Str(f, "severity")) >= min {
			out = append(out, f)
		}
	}
	return out, nil
}

func (s *Shield) Overview(ctx context.Context) (JSON, error) {
	return s.c.get(ctx, shieldBase+"/overview", nil)
}

// Vibe scans.
func (s *Shield) VibeProjects(ctx context.Context) (JSON, error) {
	return s.c.get(ctx, shieldBase+"/vibe/projects", nil)
}
func (s *Shield) VibeScan(ctx context.Context, project int) (JSON, error) {
	return s.c.post(ctx, idPath(shieldBase+"/vibe/projects", project, "scan"), nil)
}
func (s *Shield) VibeScanDetail(ctx context.Context, scan int) (JSON, error) {
	return s.c.get(ctx, idPath(shieldBase+"/vibe/scans", scan), nil)
}

// WaitVibeScan polls a vibe scan until it is done or failed and returns the detail (with findings).
func (s *Shield) WaitVibeScan(ctx context.Context, scan int, timeout time.Duration) (JSON, error) {
	return s.c.pollUntil(ctx, timeout, func() (JSON, bool, error) {
		j, err := s.VibeScanDetail(ctx, scan)
		if err != nil {
			return nil, false, err
		}
		st := Str(j, "status")
		return j, st == "done" || st == "failed", nil
	})
}

// Secret watch.
func (s *Shield) Secrets(ctx context.Context) (JSON, error) {
	return s.c.get(ctx, shieldBase+"/secrets", nil)
}
func (s *Shield) SecretSources(ctx context.Context, kind, ref string) (JSON, error) {
	return s.c.post(ctx, shieldBase+"/secrets/sources", JSON{"kind": kind, "ref": ref})
}
func (s *Shield) SecretScan(ctx context.Context, source int) (JSON, error) {
	return s.c.post(ctx, idPath(shieldBase+"/secrets/sources", source, "scan"), nil)
}

// SecretFindings lists findings; status may be empty or open, acknowledged, revoked, false_positive.
func (s *Shield) SecretFindings(ctx context.Context, status string) (JSON, error) {
	q := Query{}
	if status != "" {
		q["status"] = status
	}
	return s.c.get(ctx, shieldBase+"/secrets/findings", q)
}

// Pentest.
func (s *Shield) Pentest(ctx context.Context) (JSON, error) {
	return s.c.get(ctx, shieldBase+"/pentest", nil)
}
func (s *Shield) PentestScan(ctx context.Context, target int) (JSON, error) {
	return s.c.post(ctx, idPath(shieldBase+"/pentest/targets", target, "scan"), nil)
}
func (s *Shield) PentestScanDetail(ctx context.Context, scan int) (JSON, error) {
	return s.c.get(ctx, idPath(shieldBase+"/pentest/scans", scan), nil)
}
func (s *Shield) PentestHistory(ctx context.Context, target int) (JSON, error) {
	return s.c.get(ctx, idPath(shieldBase+"/pentest/targets", target, "history"), nil)
}
func (s *Shield) PentestRetest(ctx context.Context, finding int) (JSON, error) {
	return s.c.post(ctx, idPath(shieldBase+"/pentest/findings", finding, "retest"), nil)
}

// WaitPentestScan polls a pentest scan until done or failed.
func (s *Shield) WaitPentestScan(ctx context.Context, scan int, timeout time.Duration) (JSON, error) {
	return s.c.pollUntil(ctx, timeout, func() (JSON, bool, error) {
		j, err := s.PentestScanDetail(ctx, scan)
		if err != nil {
			return nil, false, err
		}
		st := Str(j, "status")
		return j, st == "done" || st == "failed", nil
	})
}

// WAF.
func (s *Shield) Waf(ctx context.Context) (JSON, error) { return s.c.get(ctx, shieldBase+"/waf", nil) }
func (s *Shield) WafEvents(ctx context.Context, domain int) (JSON, error) {
	return s.c.get(ctx, idPath(shieldBase+"/waf/domains", domain, "events"), nil)
}

// Phishing simulation.
func (s *Shield) Phishing(ctx context.Context) (JSON, error) {
	return s.c.get(ctx, shieldBase+"/phishing", nil)
}
func (s *Shield) PhishingCampaign(ctx context.Context, id int) (JSON, error) {
	return s.c.get(ctx, idPath(shieldBase+"/phishing/campaigns", id), nil)
}
func (s *Shield) CreatePhishingCampaign(ctx context.Context, body JSON) (JSON, error) {
	return s.c.post(ctx, shieldBase+"/phishing/campaigns", body)
}

// Security (site scans under /api/v1/security/).
func (s *Shield) SecurityOverview(ctx context.Context) (JSON, error) {
	return s.c.get(ctx, securityBase+"/overview", nil)
}
func (s *Shield) SecurityScan(ctx context.Context, target int) (JSON, error) {
	return s.c.post(ctx, securityBase+"/scans", JSON{"target": target})
}
func (s *Shield) SecurityScanDetail(ctx context.Context, scan int) (JSON, error) {
	return s.c.get(ctx, idPath(securityBase+"/scans", scan), nil)
}
