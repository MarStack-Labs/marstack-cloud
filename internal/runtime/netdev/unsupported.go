//go:build !linux

package netdev

import "errors"

var errUnsupported = errors.New("the network datapath needs Linux")

func EnsureBridge(string, string) error {
	return errUnsupported
}

func EnsureEgress(string, string) error {
	return errUnsupported
}

func Attach(int, Interface) error {
	return errUnsupported
}

func Detach(string) error {
	return errUnsupported
}
