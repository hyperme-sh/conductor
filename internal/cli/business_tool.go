package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/hyperme-sh/hypermesh-conductor/internal/businesstool"
)

func businessToolCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("business-tool", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "absolute path to the trusted connection binding")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *configPath == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, businesstool.Invalid)
		return 2
	}
	result, err := runBusinessTool(*configPath, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writePretty(stdout, result)
}

func runBusinessTool(configPath string, stdin io.Reader) (businesstool.Result, error) {
	config, err := businesstool.ReadConfig(configPath)
	if err != nil {
		return businesstool.Result{}, err
	}
	call, err := businesstool.ReadCall(stdin)
	if err != nil {
		return businesstool.Result{}, err
	}
	reviewer, err := businesstool.NewAuthority(os.Getenv("AIUDITOR_WORKSPACE_CONNECTION_URL"),
		os.Getenv("AIUDITOR_WORKSPACE_CONNECTION_KEY"), nil)
	if err != nil {
		return businesstool.Result{}, err
	}
	runner := businesstool.Runner{Binding: config.Binding, Review: reviewer}
	switch config.Binding.Provider {
	case "odoo":
		runner.Odoo, err = businesstool.NewOdoo(os.Getenv("SUPPORT_API_URL"), os.Getenv("SUPPORT_READER_TOKEN"), nil)
	case "outlook":
		runner.Outlook, err = businesstool.NewOutlook(config.OutlookFixture, config.OutlookState, config.OutlookFolders)
	default:
		err = businesstool.Invalid
	}
	if err != nil {
		return businesstool.Result{}, err
	}
	return runner.Run(context.Background(), call)
}
