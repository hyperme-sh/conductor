package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/hyperme-sh/hypermesh-conductor/internal/accesspreview"
	"github.com/hyperme-sh/hypermesh-conductor/internal/capabilitydispatch"
	"github.com/hyperme-sh/hypermesh-conductor/internal/codingloop"
	"github.com/hyperme-sh/hypermesh-conductor/internal/machineingress"
	"io"
)

func codingRunCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coding-run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	plane := fs.String("plane", "", "trusted Plane origin")
	authority := fs.String("authority", "", "trusted AIuditor action endpoint")
	dispatchConfig := fs.String("config", "", "trusted dispatch configuration")
	socket := fs.String("socket", "", "private Conductor socket")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	var input struct {
		Credential string                      `json:"credential"`
		Target     machineingress.CodingTarget `json:"target"`
	}
	raw, e := io.ReadAll(io.LimitReader(stdin, 32769))
	if e != nil || len(raw) > 32768 {
		return 1
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		return 1
	}
	client := machineingress.Client{Origin: *plane}
	var hooks machineingress.CodingHooks
	if input.Target.Options != nil {
		cfg, err := capabilitydispatch.Load(*dispatchConfig)
		if err != nil {
			return 1
		}
		journal, err := capabilitydispatch.OpenJournal(cfg.Journal)
		if err != nil {
			return 1
		}
		defer journal.Close()
		hooks = &codingloop.Loop{Client: client, Policy: accesspreview.Client{Plane: client, Endpoint: *authority}, Token: input.Credential, Options: *input.Target.Options, Journal: journal}
	}
	result, e := client.ContinueWithHooks(context.Background(), input.Credential, *socket, input.Target, hooks)
	if e != nil {
		fmt.Fprintln(stderr, "Assigned coding task unavailable; no retry")
		return 1
	}
	return writePretty(stdout, result)
}
