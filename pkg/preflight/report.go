package preflight

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

type Severity string

const (
	SeverityOK    Severity = "ok"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

type Report struct {
	Edition     string            `json:"edition"`
	GeneratedAt time.Time         `json:"generated_at"`
	Overall     Severity          `json:"overall"`
	Summary     map[Severity]int  `json:"summary"`
	Checks      []Check           `json:"checks"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type Check struct {
	ID       string         `json:"id"`
	Scope    string         `json:"scope"`
	Severity Severity       `json:"severity"`
	Message  string         `json:"message"`
	Action   string         `json:"action,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

func NewReport(edition string, now time.Time) Report {
	return Report{
		Edition:     edition,
		GeneratedAt: now.UTC(),
		Overall:     SeverityOK,
		Summary: map[Severity]int{
			SeverityOK:    0,
			SeverityWarn:  0,
			SeverityError: 0,
		},
	}
}

func (r *Report) Add(check Check) {
	if check.Scope == "" {
		check.Scope = "general"
	}
	if check.Severity == "" {
		check.Severity = SeverityOK
	}
	r.Checks = append(r.Checks, check)
	r.recalculate()
}

func (r *Report) Append(checks ...Check) {
	r.Checks = append(r.Checks, checks...)
	r.recalculate()
}

func (r *Report) HasErrors() bool {
	return r.Overall == SeverityError
}

func (r *Report) recalculate() {
	summary := map[Severity]int{
		SeverityOK:    0,
		SeverityWarn:  0,
		SeverityError: 0,
	}
	overall := SeverityOK
	for i := range r.Checks {
		if r.Checks[i].Scope == "" {
			r.Checks[i].Scope = "general"
		}
		if r.Checks[i].Severity == "" {
			r.Checks[i].Severity = SeverityOK
		}
		summary[r.Checks[i].Severity]++
		switch r.Checks[i].Severity {
		case SeverityError:
			overall = SeverityError
		case SeverityWarn:
			if overall == SeverityOK {
				overall = SeverityWarn
			}
		}
	}
	r.Overall = overall
	r.Summary = summary
}

func WriteJSON(w io.Writer, report Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func WriteHuman(w io.Writer, report Report) error {
	if _, err := fmt.Fprintf(w, "HistorySync %s deployment preflight\n", report.Edition); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Generated: %s\n", report.GeneratedAt.Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Overall: %s  ok=%d warn=%d error=%d\n\n",
		report.Overall,
		report.Summary[SeverityOK],
		report.Summary[SeverityWarn],
		report.Summary[SeverityError],
	); err != nil {
		return err
	}

	checks := append([]Check(nil), report.Checks...)
	sort.SliceStable(checks, func(i, j int) bool {
		if checks[i].Scope != checks[j].Scope {
			return checks[i].Scope < checks[j].Scope
		}
		return checks[i].ID < checks[j].ID
	})
	for _, check := range checks {
		if _, err := fmt.Fprintf(w, "[%s] %s/%s: %s\n", check.Severity, check.Scope, check.ID, check.Message); err != nil {
			return err
		}
		if check.Action != "" {
			if _, err := fmt.Fprintf(w, "  action: %s\n", check.Action); err != nil {
				return err
			}
		}
		if len(check.Details) > 0 {
			keys := make([]string, 0, len(check.Details))
			for key := range check.Details {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, key := range keys {
				parts = append(parts, fmt.Sprintf("%s=%v", key, check.Details[key]))
			}
			if _, err := fmt.Fprintf(w, "  details: %s\n", strings.Join(parts, ", ")); err != nil {
				return err
			}
		}
	}
	return nil
}
