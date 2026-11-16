package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/hyperme-sh/hypermesh-conductor/internal/machineingress"
	"io"
)

func machinePromptCmd(action string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	plane := fs.String("plane", "", "trusted Plane HTTPS origin")
	socket := fs.String("socket", "", "private Conductor socket")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	raw, e := io.ReadAll(io.LimitReader(stdin, 262145))
	if e != nil || len(raw) > 262144 {
		return 1
	}
	var input struct {
		Credential string                 `json:"credential"`
		Binding    machineingress.Binding `json:"binding"`
		Submission string                 `json:"submission_base64"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	fail := func() int { fmt.Fprintln(stderr, "Assigned machine prompt unavailable"); return 1 }
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		return fail()
	}
	if action == "machine-context" {
		fact, e := (machineingress.Client{Origin: *plane}).Check(context.Background(), input.Credential, input.Binding, "", "", "", "")
		if e != nil {
			return fail()
		}
		return writePretty(stdout, fact)
	}
	submission, e := base64.StdEncoding.DecodeString(input.Submission)
	if e != nil {
		return fail()
	}
	result, e := machineingress.Submit(context.Background(), *socket, input.Credential, submission)
	if e != nil {
		return fail()
	}
	if _, e = stdout.Write(result); e != nil {
		return 1
	}
	return 0
}
