// Command kubectl-tuggy is the tuggy kubectl plugin. kubectl runs it when the
// user types "kubectl tuggy ...".
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/tuggy-dev/kubectl-tuggy/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], cli.StdStreams())
	stop()
	os.Exit(code)
}
