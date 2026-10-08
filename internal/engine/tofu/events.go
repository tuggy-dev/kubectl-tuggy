package tofu

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
)

// EventType says what an Event reports.
type EventType string

// Event types, translated from OpenTofu's machine-readable output
// (https://opentofu.org/docs/internals/machine-readable-ui/).
const (
	EventPlannedChange    EventType = "planned_change" // a resource will be changed
	EventResourceStart    EventType = "resource_start" // a change to a resource started
	EventResourceProgress EventType = "resource_progress"
	EventResourceDone     EventType = "resource_done"
	EventResourceFailed   EventType = "resource_failed"
	EventSummary          EventType = "summary"    // totals for a plan or apply
	EventDiagnostic       EventType = "diagnostic" // an error or warning
)

// Event is one step of an OpenTofu run, for progress display.
type Event struct {
	Type EventType

	// Resource is the resource address, for example "google_container_cluster.this".
	Resource string

	// ResourceType is the resource's type, for example "google_container_cluster".
	ResourceType string

	// Action is what happens to the resource: create, update, delete, replace, read.
	Action string

	// Elapsed is how long the change has been running.
	Elapsed time.Duration

	// Message is OpenTofu's own one-line description.
	Message string

	Summary    *ChangeSummary
	Diagnostic *Diagnostic
}

// ChangeSummary counts the resources a plan or apply changes.
type ChangeSummary struct {
	Add       int    `json:"add"`
	Change    int    `json:"change"`
	Remove    int    `json:"remove"`
	Import    int    `json:"import"`
	Operation string `json:"operation"` // "plan", "apply" or "destroy"
}

// Total is the number of resources affected.
func (s ChangeSummary) Total() int { return s.Add + s.Change + s.Remove + s.Import }

// Diagnostic is an error or warning reported by OpenTofu.
type Diagnostic struct {
	Severity string `json:"severity"` // "error" or "warning"
	Summary  string `json:"summary"`
	Detail   string `json:"detail"`
}

// rawEvent is one line of OpenTofu's -json output.
type rawEvent struct {
	Level   string `json:"@level"`
	Message string `json:"@message"`
	Type    string `json:"type"`

	Change *struct {
		Resource rawResource `json:"resource"`
		Action   string      `json:"action"`
	} `json:"change"`

	Hook *struct {
		Resource       rawResource `json:"resource"`
		Action         string      `json:"action"`
		ElapsedSeconds float64     `json:"elapsed_seconds"`
	} `json:"hook"`

	Changes    *ChangeSummary `json:"changes"`
	Diagnostic *Diagnostic    `json:"diagnostic"`
}

type rawResource struct {
	Addr         string `json:"addr"`
	ResourceType string `json:"resource_type"`
}

// translate turns a raw line into an Event, or false if it isn't one tuggy
// reports.
func (r rawEvent) translate() (Event, bool) {
	ev := Event{Message: r.Message}
	switch r.Type {
	case "planned_change":
		if r.Change == nil {
			return ev, false
		}
		ev.Type = EventPlannedChange
		ev.Resource, ev.ResourceType, ev.Action = r.Change.Resource.Addr, r.Change.Resource.ResourceType, r.Change.Action
	case "apply_start", "apply_progress", "apply_complete", "apply_errored":
		if r.Hook == nil {
			return ev, false
		}
		ev.Type = map[string]EventType{
			"apply_start":    EventResourceStart,
			"apply_progress": EventResourceProgress,
			"apply_complete": EventResourceDone,
			"apply_errored":  EventResourceFailed,
		}[r.Type]
		ev.Resource, ev.ResourceType, ev.Action = r.Hook.Resource.Addr, r.Hook.Resource.ResourceType, r.Hook.Action
		ev.Elapsed = time.Duration(r.Hook.ElapsedSeconds * float64(time.Second))
	case "change_summary":
		if r.Changes == nil {
			return ev, false
		}
		ev.Type, ev.Summary = EventSummary, r.Changes
	case "diagnostic":
		if r.Diagnostic == nil {
			return ev, false
		}
		ev.Type, ev.Diagnostic = EventDiagnostic, r.Diagnostic
	default:
		return ev, false
	}
	return ev, true
}

// eventWriter receives OpenTofu's stdout. It copies every line to the log,
// parses -json lines into Events for onEvent, and remembers the summary and
// error diagnostics for the result of the run.
type eventWriter struct {
	log     io.Writer
	onEvent func(Event)

	mu          sync.Mutex
	partial     []byte
	summary     *ChangeSummary
	diagnostics []Diagnostic
}

func newEventWriter(log io.Writer, onEvent func(Event)) *eventWriter {
	if log == nil {
		log = io.Discard
	}
	return &eventWriter{log: log, onEvent: onEvent}
}

func (w *eventWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		w.line(w.partial[:i])
		w.partial = w.partial[i+1:]
	}
	return len(p), nil
}

// Close handles a final line without a trailing newline.
func (w *eventWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.partial) > 0 {
		w.line(w.partial)
		w.partial = nil
	}
	return nil
}

func (w *eventWriter) line(b []byte) {
	_, _ = w.log.Write(append(append([]byte(nil), b...), '\n'))

	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return
	}
	var raw rawEvent
	if json.Unmarshal(trimmed, &raw) != nil {
		return
	}
	ev, ok := raw.translate()
	if !ok {
		return
	}
	switch ev.Type {
	case EventSummary:
		w.summary = ev.Summary
	case EventDiagnostic:
		if ev.Diagnostic.Severity == "error" {
			w.diagnostics = append(w.diagnostics, *ev.Diagnostic)
		}
	}
	if w.onEvent != nil {
		w.onEvent(ev)
	}
}

func (w *eventWriter) result() (*ChangeSummary, []Diagnostic) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.summary, append([]Diagnostic(nil), w.diagnostics...)
}

// tailWriter copies to dst and keeps the last few lines, so an error message
// can quote them when OpenTofu fails without structured diagnostics.
type tailWriter struct {
	dst   io.Writer
	mu    sync.Mutex
	lines []string
	max   int
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	for line := range strings.Lines(string(p)) {
		if s := strings.TrimSpace(line); s != "" {
			t.lines = append(t.lines, s)
		}
	}
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
	t.mu.Unlock()
	return t.dst.Write(p)
}

func (t *tailWriter) tail() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.lines...)
}
