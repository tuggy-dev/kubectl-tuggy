package kind

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	kindlog "sigs.k8s.io/kind/pkg/log"

	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

// progressLogger is a kind logger that writes everything kind says to the
// operation's log, and turns kind's status lines ("Preparing nodes",
// "Starting control-plane", ...) into progress updates, so the CLI shows the
// same progress as for other platforms.
type progressLogger struct {
	log        io.Writer
	onProgress func(platform.Progress)
}

var _ kindlog.Logger = (*progressLogger)(nil)

func newProgressLogger(log io.Writer, onProgress func(platform.Progress)) *progressLogger {
	if log == nil {
		log = io.Discard
	}
	return &progressLogger{log: log, onProgress: onProgress}
}

func (l *progressLogger) Warn(message string) { l.write("WARNING: " + message) }
func (l *progressLogger) Warnf(format string, args ...any) {
	l.Warn(fmt.Sprintf(format, args...))
}
func (l *progressLogger) Error(message string) { l.write("ERROR: " + message) }
func (l *progressLogger) Errorf(format string, args ...any) {
	l.Error(fmt.Sprintf(format, args...))
}

// V returns an info logger. Level 0 is what kind shows users; higher levels
// are debug output, which goes to the log only.
func (l *progressLogger) V(level kindlog.Level) kindlog.InfoLogger {
	return infoLogger{l: l, status: level == 0}
}

func (l *progressLogger) write(message string) {
	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}
	_, _ = io.WriteString(l.log, message)
}

type infoLogger struct {
	l      *progressLogger
	status bool
}

func (i infoLogger) Enabled() bool { return true }

func (i infoLogger) Infof(format string, args ...any) { i.Info(fmt.Sprintf(format, args...)) }

func (i infoLogger) Info(message string) {
	i.l.write(message)
	if !i.status || i.l.onProgress == nil {
		return
	}
	if step, ok := statusStep(message); ok {
		i.l.onProgress(platform.Progress{Kind: platform.ProgressInfo, Message: step})
	}
}

// statusStep extracts the step from a kind status line such as
// " • Preparing nodes 📦  ...". Other lines, such as the " ✓ ..." that ends
// a step, are not steps.
func statusStep(line string) (string, bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "•") {
		return "", false
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "•"))
	s = strings.TrimSuffix(s, "...")
	s = strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			return -1 // emoji and variation selectors
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	return s, s != ""
}
