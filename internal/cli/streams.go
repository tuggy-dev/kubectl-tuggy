package cli

import (
	"io"
	"os"
)

// IOStreams holds the standard streams so commands can be tested with buffers.
type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

// StdStreams returns streams connected to the process's stdin, stdout and stderr.
func StdStreams() IOStreams {
	return IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}
}
