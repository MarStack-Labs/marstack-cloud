//go:build linux

package cli

import (
	"syscall"
	"unsafe"
)

func makeRaw(fd int) (func(), bool) {
	var saved syscall.Termios
	if err := termios(fd, syscall.TCGETS, &saved); err != nil {
		return func() {}, false
	}

	raw := saved
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0

	if err := termios(fd, syscall.TCSETS, &raw); err != nil {
		return func() {}, false
	}

	return func() { _ = termios(fd, syscall.TCSETS, &saved) }, true
}

func termios(fd int, request uintptr, settings *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request,
		uintptr(unsafe.Pointer(settings)))
	if errno != 0 {
		return errno
	}
	return nil
}
