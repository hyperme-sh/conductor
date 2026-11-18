//go:build !linux

package codingloop

import "context"

func readFile(string, string, uint32) ([]byte, error) { return nil, Err }
func replaceFile(string, string, uint32, string, []byte, func(string) error, context.Context) error {
	return Err
}
func runCheck(context.Context, Check, []file) (string, bool, error) { return "", false, Err }

func reviewedRead(string, string, uint32, func(int64, string) error, context.Context) ([]byte, error) {
	return nil, Err
}
