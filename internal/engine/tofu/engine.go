package tofu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
)

const (
	// VariablesFile holds a cluster's module variables: the single record of
	// what the user asked for. OpenTofu does not load it automatically (only
	// terraform.tfvars.json and *.auto.tfvars.json are); the engine reads it
	// on the host and passes each value as a TF_VAR_ environment variable.
	//
	// Engines that run in a VM (Colima, Docker Desktop) show changes made on
	// the host to containers up to about a second late, so a container could
	// read a stale or missing file right after it is rewritten. Environment
	// variables avoid that. To use the file by hand:
	//
	//	tofu plan -var-file=tuggy.tfvars.json
	VariablesFile = "tuggy.tfvars.json"

	// planFilePrefix names saved plans. Each plan gets a unique name so a
	// container never reads a name the host just removed. Plans can contain
	// secrets, so they are removed after use.
	planFilePrefix = "tfplan-"

	// Paths inside the container.
	workspaceMount   = "/workspace"
	pluginCacheMount = "/plugin-cache"

	// ClusterLabel is set on every OpenTofu container, naming the cluster.
	ClusterLabel = "dev.tuggy.cluster"

	dirPerm  = 0o700
	filePerm = 0o600
)

// Engine runs OpenTofu commands in containers. It knows nothing about any
// cloud; platforms give it a module, variables and credentials.
type Engine struct {
	Runner runner.Runner

	// Image is the OpenTofu image. Empty means Image().
	Image string

	// PluginCacheDir is a host directory shared by every cluster, so
	// OpenTofu providers are downloaded once. Empty disables the cache.
	PluginCacheDir string
}

// Workspace is one cluster's OpenTofu working directory and how to run in it.
type Workspace struct {
	// Name of the cluster, used to label containers.
	Name string

	// Dir is the absolute host directory holding the module, variables
	// file and state.
	Dir string

	// Mounts and Env pass credentials into the container. Platforms supply
	// them.
	Mounts []runner.Mount
	Env    map[string]string

	// Log receives the complete output of every command.
	Log io.Writer

	// OnEvent, if set, receives progress events.
	OnEvent func(Event)
}

// PlanResult describes a saved plan.
type PlanResult struct {
	// Summary counts what the plan changes.
	Summary ChangeSummary

	// Changes lists each planned resource change.
	Changes []Event

	// File is the saved plan's name in the workspace, applied by Apply.
	File string
}

// HasChanges reports whether applying the plan would change anything.
func (p *PlanResult) HasChanges() bool { return p.Summary.Total() > 0 }

// Output is one OpenTofu output value.
type Output struct {
	Value     json.RawMessage `json:"value"`
	Sensitive bool            `json:"sensitive"`
}

// Error is a failed OpenTofu command.
type Error struct {
	Command     string
	ExitCode    int
	Diagnostics []Diagnostic

	// Tail holds the last lines of stderr, used when there are no diagnostics.
	Tail []string
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "tofu %s failed", e.Command)
	switch {
	case len(e.Diagnostics) > 0:
		for i, d := range e.Diagnostics {
			if i == 0 {
				b.WriteString(": ")
			} else {
				b.WriteString("; ")
			}
			b.WriteString(d.Summary)
			if detail := firstLine(d.Detail); detail != "" {
				b.WriteString(": " + detail)
			}
		}
	case len(e.Tail) > 0:
		b.WriteString(": " + e.Tail[len(e.Tail)-1])
	default:
		fmt.Fprintf(&b, " with exit code %d", e.ExitCode)
	}
	return b.String()
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

// WriteModule copies a platform's module files into dir. If dir already holds
// .tf files they are kept, so a cluster keeps using the module version it was
// built with.
func WriteModule(dir string, module fs.FS) error {
	existing, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return err
	}
	return fs.WalkDir(module, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, dirPerm)
		}
		if !isModuleFile(p) {
			return nil
		}
		data, err := fs.ReadFile(module, p)
		if err != nil {
			return err
		}
		return writeFileAtomic(target, data)
	})
}

// isModuleFile keeps OpenTofu configuration and the provider lock file, and
// skips anything else an embedded module directory might contain.
func isModuleFile(p string) bool {
	base := path.Base(p)
	return strings.HasSuffix(base, ".tf") || strings.HasSuffix(base, ".tf.json") || base == ".terraform.lock.hcl"
}

// WriteVariables writes vars, which must encode to a JSON object, as the
// workspace's variables file, replacing any previous file.
func WriteVariables(dir string, vars any) error {
	data, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding OpenTofu variables: %w", err)
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, VariablesFile), append(data, '\n'))
}

// Init runs "tofu init", downloading providers.
func (e *Engine) Init(ctx context.Context, ws Workspace) error {
	_, _, err := e.run(ctx, ws, "init", []string{"init", "-input=false", "-json"}, exitOK)
	return err
}

// PlanOptions control Plan.
type PlanOptions struct {
	// Destroy plans removal of every resource.
	Destroy bool
}

// Plan runs "tofu plan" and saves the plan for Apply.
func (e *Engine) Plan(ctx context.Context, ws Workspace, opts PlanOptions) (*PlanResult, error) {
	file := planFilePrefix + strconv.FormatInt(time.Now().UnixNano(), 36)
	args := []string{"plan", "-input=false", "-json", "-detailed-exitcode", "-out=" + file}
	if opts.Destroy {
		args = append(args, "-destroy")
	}

	var changes []Event
	collect := ws
	collect.OnEvent = func(ev Event) {
		if ev.Type == EventPlannedChange {
			changes = append(changes, ev)
		}
		if ws.OnEvent != nil {
			ws.OnEvent(ev)
		}
	}

	// With -detailed-exitcode, 0 means no changes and 2 means changes.
	summary, _, err := e.run(ctx, collect, "plan", args, func(code int) bool { return code == 0 || code == 2 })
	if err != nil {
		_ = removePlan(ws.Dir, file)
		return nil, err
	}
	result := &PlanResult{Changes: changes, File: file}
	if summary != nil {
		result.Summary = *summary
	}
	return result, nil
}

// Apply runs "tofu apply" on a plan saved by Plan, so exactly what was
// planned is applied. The saved plan is removed afterwards.
func (e *Engine) Apply(ctx context.Context, ws Workspace, plan *PlanResult) (*ChangeSummary, error) {
	if plan == nil || plan.File == "" {
		return nil, errors.New("no saved plan to apply; run Plan first")
	}
	if _, err := os.Stat(filepath.Join(ws.Dir, plan.File)); err != nil {
		return nil, fmt.Errorf("saved plan %s is missing; run Plan again", plan.File)
	}
	defer func() { _ = e.DiscardPlan(ws, plan) }()

	summary, _, err := e.run(ctx, ws, "apply", []string{"apply", "-input=false", "-json", plan.File}, exitOK)
	if err != nil {
		return nil, err
	}
	if summary == nil {
		summary = &ChangeSummary{Operation: "apply"}
	}
	return summary, nil
}

// Outputs runs "tofu output" and returns every output value.
func (e *Engine) Outputs(ctx context.Context, ws Workspace) (map[string]Output, error) {
	var stdout strings.Builder
	quiet := ws
	quiet.OnEvent = nil
	_, _, err := e.runTo(ctx, quiet, "output", []string{"output", "-json"}, exitOK, &stdout)
	if err != nil {
		return nil, err
	}
	outputs := map[string]Output{}
	if err := json.Unmarshal([]byte(stdout.String()), &outputs); err != nil {
		return nil, fmt.Errorf("reading tofu output: %w", err)
	}
	return outputs, nil
}

// DiscardPlan removes a saved plan without applying it. Plans can contain
// secrets.
func (e *Engine) DiscardPlan(ws Workspace, plan *PlanResult) error {
	if plan == nil || plan.File == "" {
		return nil
	}
	return removePlan(ws.Dir, plan.File)
}

func removePlan(dir, file string) error {
	err := os.Remove(filepath.Join(dir, file))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// variablesEnv reads the workspace's variables file and returns one TF_VAR_
// environment variable per value. Strings are passed as they are; other
// values as JSON, which OpenTofu parses for lists, objects, numbers and bools.
// Null values are left out so the module's default applies.
func variablesEnv(dir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, VariablesFile)) // #nosec G304 -- the cluster's own workspace
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var vars map[string]json.RawMessage
	if err := json.Unmarshal(data, &vars); err != nil {
		return nil, fmt.Errorf("reading %s: %w", VariablesFile, err)
	}
	env := make(map[string]string, len(vars))
	for name, raw := range vars {
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return nil, fmt.Errorf("reading %s: %w", VariablesFile, err)
		}
		value := compact.String()
		if value == "null" {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			value = s
		}
		env["TF_VAR_"+name] = value
	}
	return env, nil
}

// StringOutputs returns the outputs whose values are strings, for saving in a
// cluster record. Sensitive outputs are left out.
func StringOutputs(outputs map[string]Output) map[string]string {
	strs := map[string]string{}
	for name, o := range outputs {
		if o.Sensitive {
			continue
		}
		var s string
		if json.Unmarshal(o.Value, &s) == nil {
			strs[name] = s
		}
	}
	return strs
}

func exitOK(code int) bool { return code == 0 }

// run executes one OpenTofu command with JSON output parsed into events.
func (e *Engine) run(ctx context.Context, ws Workspace, name string, args []string, ok func(int) bool) (*ChangeSummary, []Diagnostic, error) {
	events := newEventWriter(ws.Log, ws.OnEvent)
	summary, diags, err := e.runTo(ctx, ws, name, args, ok, events)
	return summary, diags, err
}

// runTo executes one OpenTofu command with stdout sent to stdout. When stdout
// is an eventWriter, its summary and diagnostics are returned.
func (e *Engine) runTo(ctx context.Context, ws Workspace, name string, args []string, ok func(int) bool, stdout io.Writer) (*ChangeSummary, []Diagnostic, error) {
	spec, err := e.spec(ws, args)
	if err != nil {
		return nil, nil, err
	}

	log := ws.Log
	if log == nil {
		log = io.Discard
	}
	fmt.Fprintf(log, "$ tofu %s\n", strings.Join(args, " "))
	stderr := &tailWriter{dst: log, max: 5}
	spec.Stdout, spec.Stderr = stdout, stderr

	code, runErr := e.Runner.Run(ctx, spec)

	var summary *ChangeSummary
	var diags []Diagnostic
	if ew, isEvents := stdout.(*eventWriter); isEvents {
		_ = ew.Close()
		summary, diags = ew.result()
	}

	if runErr != nil {
		return summary, diags, fmt.Errorf("tofu %s: %w", name, runErr)
	}
	if !ok(code) {
		return summary, diags, &Error{Command: name, ExitCode: code, Diagnostics: diags, Tail: stderr.tail()}
	}
	return summary, diags, nil
}

func (e *Engine) spec(ws Workspace, args []string) (runner.Spec, error) {
	if e.Runner == nil {
		return runner.Spec{}, errors.New("tofu engine has no runner")
	}
	if !filepath.IsAbs(ws.Dir) {
		return runner.Spec{}, fmt.Errorf("workspace directory %q must be absolute", ws.Dir)
	}

	image := e.Image
	if image == "" {
		image = Image()
	}

	env := map[string]string{
		// OpenTofu needs a writable home when running as a non-root user.
		"HOME":               "/tmp",
		"TF_IN_AUTOMATION":   "1",
		"TF_INPUT":           "0",
		"CHECKPOINT_DISABLE": "1",
	}
	mounts := []runner.Mount{{Type: runner.MountBind, Source: ws.Dir, Target: workspaceMount}}
	if e.PluginCacheDir != "" {
		if err := os.MkdirAll(e.PluginCacheDir, dirPerm); err != nil {
			return runner.Spec{}, fmt.Errorf("creating plugin cache: %w", err)
		}
		mounts = append(mounts, runner.Mount{Type: runner.MountBind, Source: e.PluginCacheDir, Target: pluginCacheMount})
		env["TF_PLUGIN_CACHE_DIR"] = pluginCacheMount
	}
	vars, err := variablesEnv(ws.Dir)
	if err != nil {
		return runner.Spec{}, err
	}
	maps.Copy(env, vars)
	maps.Copy(env, ws.Env)

	return runner.Spec{
		Image:      image,
		Cmd:        args,
		WorkingDir: workspaceMount,
		Env:        env,
		Mounts:     append(mounts, ws.Mounts...),
		Labels:     map[string]string{ClusterLabel: ws.Name},
		User:       runner.CallerUser(),
	}, nil
}

// writeFileAtomic writes to a new file and renames it into place. Readers,
// including containers seeing the file through a VM's file sharing, never see
// a partly written or stale file.
func writeFileAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp.Name(), filePerm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
