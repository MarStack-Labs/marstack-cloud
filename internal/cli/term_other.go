//go:build !linux

package cli

func makeRaw(int) (func(), bool) {
	return func() {}, false
}
