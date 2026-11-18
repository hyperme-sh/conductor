package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/hyperme-sh/hypermesh-conductor/internal/machineingress"
	"io"
)

func codingRunCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coding-run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	plane := fs.String("plane", "", "trusted Plane origin")
	socket := fs.String("socket", "", "private Conductor socket")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	var input struct {
		Credential string                      `json:"credential"`
		Target     machineingress.CodingTarget `json:"target"`
	}
	raw, e := io.ReadAll(io.LimitReader(stdin, 16385))
	if e != nil || len(raw) > 16384 {
		return 1
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		return 1
	}
	result, e := (machineingress.Client{Origin: *plane}).ContinueCoding(context.Background(), input.Credential, *socket, input.Target)
	if e != nil {
		fmt.Fprintln(stderr, "Assigned coding task unavailable; no retry")
		return 1
	}
	return writePretty(stdout, result)
}
