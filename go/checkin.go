package linearjeph

import (
	"fmt"
	"time"
)

// CheckinOptions describes a job for the cron monitor. All fields are optional.
type CheckinOptions struct {
	RunID      string        // pair a "start" with its "ok"/"fail"; Monitor sets it for you
	Duration   time.Duration // run time (Monitor measures it)
	Message    string        // failure reason (scrubbed)
	Schedule   string        // cron expression, e.g. "0 2 * * *": enables missed-run alerts
	TZ         string        // IANA zone the schedule is written in, default UTC
	Grace      time.Duration // how late a run may be before it counts as missed
	IntervalS  int           // for interval-style jobs without a cron schedule
	MaxRuntime time.Duration // alert when a started run does not finish within this time
}

// Checkin reports a job run: status is "start", "ok" or "fail". Invalid names (A-Z a-z 0-9 . _ -, max 64) are ignored.
func Checkin(name, status string, o CheckinOptions) {
	defer func() { _ = recover() }()
	s := get()
	if s == nil {
		return
	}
	if !hbName.MatchString(name) {
		dbg(s, "check-in name %q is invalid; ignored", name)
		return
	}
	if status != "start" && status != "ok" {
		status = "fail"
	}
	b := map[string]any{"monitor": name, "status": status}
	if o.RunID != "" {
		b["run_id"] = o.RunID
	}
	if o.Duration > 0 {
		b["duration_ms"] = float64(o.Duration.Microseconds()) / 1000
	}
	if o.Message != "" {
		b["message"] = s.clean(o.Message, 300)
	}
	if o.Schedule != "" {
		b["schedule"] = o.Schedule
	}
	if o.TZ != "" {
		b["tz"] = o.TZ
	}
	if o.Grace > 0 {
		b["grace_s"] = int(o.Grace.Seconds())
	}
	if o.IntervalS > 0 {
		b["interval_s"] = o.IntervalS
	}
	if o.MaxRuntime > 0 {
		b["max_runtime_s"] = int(o.MaxRuntime.Seconds())
	}
	s.enqueue(checkinPath, b)
}

// Monitor runs fn between a "start" and an "ok"/"fail" check-in (with the run duration) and returns fn's error
// unchanged. intervalS (0 = unset) is how often you expect the job. A panic is reported as a failure and re-panicked.
// Flush before the process exits.
func Monitor(name string, intervalS int, fn func() error) error {
	return MonitorWith(name, CheckinOptions{IntervalS: intervalS}, fn)
}

// MonitorWith is Monitor with a cron schedule, grace period and maximum run time.
func MonitorWith(name string, o CheckinOptions, fn func() error) (err error) {
	o.RunID = randHex(16)
	Checkin(name, "start", o)
	t0 := time.Now()
	defer func() {
		o.Duration = time.Since(t0)
		if p := recover(); p != nil {
			o.Message = fmt.Sprintf("panic: %v", p)
			Checkin(name, "fail", o)
			panic(p)
		}
		if err != nil {
			o.Message = err.Error()
			Checkin(name, "fail", o)
		} else {
			Checkin(name, "ok", o)
		}
	}()
	return fn()
}

// Release records a release marker (shown on charts; errors are linked to the release that introduced them).
// An empty version uses Options.Release.
func Release(version, commit, notes string) {
	defer func() { _ = recover() }()
	s := get()
	if s == nil {
		return
	}
	if version == "" {
		version = s.opts.Release
	}
	if version == "" {
		return
	}
	b := map[string]any{"version": version}
	if commit != "" {
		b["commit"] = commit
	}
	if notes != "" {
		b["notes"] = notes
	}
	s.enqueue(releasesPath, b)
}

// DeployOptions describes a deploy for Deploy.
type DeployOptions struct {
	Version     string // default Options.Release
	Environment string // default Options.Environment
	Status      string // "started", "success" (default) or "failed"
	URL         string // link to the CI run
	Commit      string
	Notify      bool // also notify your alert channels (email, Slack, Telegram, webhook)
}

// Deploy records a deploy of a release.
func Deploy(o DeployOptions) {
	defer func() { _ = recover() }()
	s := get()
	if s == nil {
		return
	}
	if o.Version == "" {
		o.Version = s.opts.Release
	}
	if o.Version == "" {
		return
	}
	if o.Environment == "" {
		o.Environment = s.opts.Environment
	}
	if o.Status == "" {
		o.Status = "success"
	}
	b := map[string]any{"version": o.Version, "environment": o.Environment, "status": o.Status, "notify": o.Notify}
	if o.URL != "" {
		b["url"] = o.URL
	}
	if o.Commit != "" {
		b["commit"] = o.Commit
	}
	s.enqueue(deploysPath, b)
}
