package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/tuggy-dev/kubectl-tuggy/internal/clustermeta"
	"github.com/tuggy-dev/kubectl-tuggy/internal/kubeconfig"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

// App holds what commands depend on, so tests can replace any of it.
type App struct {
	Streams IOStreams

	// Root is tuggy's home directory (~/.tuggy).
	Root  string
	Store clustermeta.Store

	Platforms *platform.Registry
	Kube      *kubeconfig.Manager

	// WaitReady checks that a new cluster answers.
	WaitReady func(ctx context.Context, cfg *clientcmdapi.Config, opts kubeconfig.ReadyOptions) (string, error)

	// Interactive reports whether the user can answer prompts.
	Interactive bool

	Now func() time.Time
}

// newApp returns an App wired to the real machine. Errors finding the home
// directory are reported when a command needs it.
func newApp(streams IOStreams) (*App, error) {
	root, err := clustermeta.DefaultRoot()
	if err != nil {
		return nil, err
	}
	return &App{
		Streams:     streams,
		Root:        root,
		Store:       clustermeta.NewLocal(root),
		Platforms:   platform.Default(),
		Kube:        kubeconfig.New(),
		WaitReady:   kubeconfig.WaitReady,
		Interactive: isTerminal(streams.In),
		Now:         time.Now,
	}, nil
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd())) // #nosec G115 -- file descriptors fit in int
}

// cacheDir is shared by all clusters for downloads.
func (a *App) cacheDir() string { return filepath.Join(a.Root, "cache") }

func (a *App) printf(format string, args ...any) { fmt.Fprintf(a.Streams.Out, format, args...) }

func (a *App) warnf(format string, args ...any) {
	fmt.Fprintf(a.Streams.ErrOut, "Warning: "+format+"\n", args...)
}

// errDeclined means the user answered no to a confirmation.
var errDeclined = errors.New("cancelled: nothing was changed")

// confirm asks a yes/no question. With yes it proceeds without asking.
// Without a terminal it refuses rather than guess.
func (a *App) confirm(question string, yes bool) error {
	if yes {
		return nil
	}
	if !a.Interactive {
		return WithExitCode(ExitUsage, errors.New("confirmation needed but no terminal is attached; re-run with --yes to proceed"))
	}
	fmt.Fprintf(a.Streams.Out, "%s [y/N]: ", question)
	answer, err := bufio.NewReader(a.Streams.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return WithExitCode(ExitCancelled, errDeclined)
}

// openLog creates a log file for one operation on a cluster.
func (a *App) openLog(name, operation string) (*os.File, error) {
	dir := filepath.Join(a.Store.Dir(name), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, a.Now().UTC().Format("2006-01-02T15-04-05")+"-"+operation+".log")
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- path built from a validated cluster name
}

// lock takes a cluster's lock, explaining a conflict.
func (a *App) lock(name string) (func() error, error) {
	unlock, err := a.Store.Lock(name)
	if errors.Is(err, clustermeta.ErrLocked) {
		return nil, fmt.Errorf("another kubectl tuggy command is working on cluster %q; wait for it to finish", name)
	}
	return unlock, err
}

// interrupted turns a record left Creating or Deleting by a tuggy process
// that died (its lock is free, so nobody is working on it) into Failed, so
// it can be resumed or deleted. The caller holds the lock.
func (a *App) interrupted(rec *clustermeta.Record) error {
	if rec.Status != clustermeta.StatusCreating && rec.Status != clustermeta.StatusDeleting {
		return nil
	}
	if err := rec.Transition(clustermeta.StatusFailed, "interrupted while "+strings.ToLower(string(rec.Status)), a.Now()); err != nil {
		return err
	}
	return a.Store.Update(rec)
}

// progressPrinter shows platform progress as a short line per step.
func (a *App) progressPrinter(verbose bool) func(platform.Progress) {
	lastRunning := map[string]time.Time{}
	return func(p platform.Progress) {
		switch p.Kind {
		case platform.ProgressInfo:
			a.printf("• %s\n", p.Message)
		case platform.ProgressStarted:
			a.printf("  %s %s ...\n", verb(p.Action, false), p.Resource)
		case platform.ProgressRunning:
			// OpenTofu reports every ten seconds; once a minute is enough.
			if verbose || a.Now().Sub(lastRunning[p.Resource]) >= time.Minute {
				lastRunning[p.Resource] = a.Now()
				a.printf("  still %s %s (%s)\n", strings.ToLower(verb(p.Action, false)), p.Resource, shortDuration(p.Elapsed))
			}
		case platform.ProgressDone:
			a.printf("  ✓ %s %s in %s\n", p.Resource, verb(p.Action, true), shortDuration(p.Elapsed))
		case platform.ProgressFailed:
			a.printf("  ✗ %s failed after %s\n", p.Resource, shortDuration(p.Elapsed))
		}
	}
}

func verb(action string, done bool) string {
	forms := map[string][2]string{
		"create":  {"Creating", "created"},
		"delete":  {"Removing", "removed"},
		"update":  {"Updating", "updated"},
		"replace": {"Replacing", "replaced"},
		"read":    {"Reading", "read"},
	}
	f, ok := forms[action]
	if !ok {
		f = [2]string{"Changing", "changed"}
	}
	if done {
		return f[1]
	}
	return f[0]
}

// shortDuration formats a duration for people: "45s", "8m31s", "3h5m", "2d4h".
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// contextName is the kubeconfig context tuggy writes for a cluster.
func contextName(cluster string) string { return kubeconfig.Prefix + cluster }

// warnExpired mentions clusters past their TTL.
func (a *App) warnExpired() {
	records, err := a.Store.List()
	if err != nil {
		return
	}
	var names []string
	for _, r := range records {
		if r.Expired(a.Now()) && r.Status != clustermeta.StatusDeleting {
			names = append(names, r.Name)
		}
	}
	if len(names) > 0 {
		a.warnf("expired clusters are still running and may be costing money: %s\n         Delete them with: kubectl tuggy delete clusters --expired", strings.Join(names, ", "))
	}
}
