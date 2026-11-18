package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"github.com/hyperme-sh/hypermesh-conductor/internal/machineingress"
	"io"
	"time"
)

func continuePlanCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("continue-plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", "", "private Conductor socket")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	var in struct {
		Credential string                   `json:"credential"`
		Input      machineingress.PlanInput `json:"input"`
	}
	raw, e := io.ReadAll(io.LimitReader(stdin, 524289))
	if e != nil || len(raw) > 524288 {
		return 1
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, e := machineingress.Plan(ctx, *socket, in.Credential, in.Input)
	if e != nil {
		return 1
	}
	_, e = stdout.Write(out)
	if e != nil {
		return 1
	}
	return 0
}
