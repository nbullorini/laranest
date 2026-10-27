//go:build !windows

package winenv

import "errors"

var errNotWindows = errors.New("only available on Windows")

func AddUserPath(dir string) (bool, error)      { return false, errNotWindows }
func RemoveUserPath(dir string) (bool, error)   { return false, errNotWindows }
func HasUserPath(dir string) bool               { return false }
func SetRunAtSignIn(name, command string) error { return errNotWindows }
func RunAtSignIn(name string) string            { return "" }
