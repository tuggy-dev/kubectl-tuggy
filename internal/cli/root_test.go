package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/tuggy-dev/kubectl-tuggy/internal/engine/tofu"
	"github.com/tuggy-dev/kubectl-tuggy/internal/version"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Run(context.Background(), args, IOStreams{In: strings.NewReader(""), Out: &out, ErrOut: &errOut})
	return code, out.String(), errOut.String()
}

func TestRunExitCodes(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{name: "no args shows help", args: nil, wantCode: ExitOK},
		{name: "help flag", args: []string{"--help"}, wantCode: ExitOK},
		{name: "version", args: []string{"version"}, wantCode: ExitOK},
		{name: "unknown flag", args: []string{"--bogus"}, wantCode: ExitUsage, wantStderr: "unknown flag: --bogus"},
		{name: "unknown command", args: []string{"bogus"}, wantCode: ExitUsage, wantStderr: `unknown command "bogus"`},
		{name: "extra argument", args: []string{"version", "extra"}, wantCode: ExitUsage, wantStderr: "unknown command"},
		{name: "bad output format", args: []string{"version", "-o", "xml"}, wantCode: ExitUsage, wantStderr: `unsupported output format "xml"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, stderr := run(t, tt.args...)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %q)", code, tt.wantCode, stderr)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.wantStderr)
			}
			if tt.wantCode == ExitOK && stderr != "" {
				t.Errorf("unexpected stderr on success: %q", stderr)
			}
		})
	}
}

func TestHelpShowsKubectlPluginName(t *testing.T) {
	_, stdout, _ := run(t, "--help")
	if !strings.Contains(stdout, "kubectl tuggy [command]") {
		t.Errorf("help should use the plugin display name, got:\n%s", stdout)
	}
}

func TestVersionOutput(t *testing.T) {
	want := version.Get()
	want.TofuImage = tofu.Image()

	t.Run("text", func(t *testing.T) {
		_, stdout, _ := run(t, "version")
		if !strings.HasPrefix(stdout, "kubectl-tuggy "+want.Version+"\n") {
			t.Errorf("unexpected text output:\n%s", stdout)
		}
	})

	t.Run("json", func(t *testing.T) {
		_, stdout, _ := run(t, "version", "-o", "json")
		var got version.Info
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, stdout)
		}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("yaml", func(t *testing.T) {
		_, stdout, _ := run(t, "version", "-o", "yaml")
		var got version.Info
		if err := yaml.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("output is not YAML: %v\n%s", err, stdout)
		}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

func TestExitCodeFor(t *testing.T) {
	base := errors.New("boom")
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"plain error", base, ExitError},
		{"explicit code", WithExitCode(ExitCloud, base), ExitCloud},
		{"wrapped explicit code", fmt.Errorf("creating: %w", WithExitCode(ExitPreflight, base)), ExitPreflight},
		{"context canceled", fmt.Errorf("apply: %w", context.Canceled), ExitCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCodeFor(tt.err); got != tt.want {
				t.Errorf("ExitCodeFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestWithExitCodeNil(t *testing.T) {
	if err := WithExitCode(ExitCloud, nil); err != nil {
		t.Errorf("WithExitCode(nil) = %v, want nil", err)
	}
}
