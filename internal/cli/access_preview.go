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
	return accessReviewCmd(false, args, stdin, stdout, stderr)
}

func accessReviewCmd(active bool, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("permission-review", flag.ContinueOnError)
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
	client := accesspreview.Client{Plane: machineingress.Client{Origin: *plane}, Endpoint: *authority}
	var result accesspreview.Result
	if active {
		result, e = client.ReviewRead(context.Background(), raw)
	} else {
		result, e = client.Preview(context.Background(), raw)
	}
	if e != nil {
		fmt.Fprintln(stderr, "permission preview unavailable")
		return 1
	}
	return writePretty(stdout, result)
}
