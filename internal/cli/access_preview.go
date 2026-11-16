package cli

import (
	"context"
	"flag"
	"fmt"
	"github.com/hyperme-sh/hypermesh-conductor/internal/accesspreview"
	"github.com/hyperme-sh/hypermesh-conductor/internal/machineingress"
	"io"
)

func accessPreviewCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("permission-preview", flag.ContinueOnError)
	fs.SetOutput(stderr)
	plane := fs.String("plane", "", "trusted HTTPS Plane origin")
	authority := fs.String("authority", "", "trusted HTTPS AIuditor preview endpoint")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	raw, e := io.ReadAll(io.LimitReader(stdin, 262145))
	if e != nil || len(raw) > 262144 {
		fmt.Fprintln(stderr, "permission preview unavailable")
		return 1
	}
	result, e := (accesspreview.Client{Plane: machineingress.Client{Origin: *plane}, Endpoint: *authority}).Preview(context.Background(), raw)
	if e != nil {
		fmt.Fprintln(stderr, "permission preview unavailable")
		return 1
	}
	return writePretty(stdout, result)
}
