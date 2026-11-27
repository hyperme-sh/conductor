//go:build !linux

package cloudworkspace

import "os"

func openGrant(string) (*os.File, error) { return nil, refused }
func checkJSON(Grant) error              { return refused }
