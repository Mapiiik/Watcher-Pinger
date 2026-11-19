//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole = kernel32.NewProc("AttachConsole")
	procAllocConsole  = kernel32.NewProc("AllocConsole")
)

// attachParentProcess is ATTACH_PARENT_PROCESS: the console of the process that started this one.
const attachParentProcess = ^uintptr(0)

// console tells whether the program writes to a console, and ownConsole whether it opened the console itself, which
// closes with the program.
var console, ownConsole bool

// openConsole gives the program, built without a console, one to write to: that of cmd or PowerShell it was started
// from, or a window of its own where it was started otherwise. Closing that window stops the pinger.
func openConsole() {
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		if r, _, _ := procAllocConsole.Call(); r == 0 {
			return
		}
		ownConsole = true
	}
	console = true

	if out, err := openDevice("CONOUT$"); err == nil {
		os.Stdout, os.Stderr = out, out
		_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(out.Fd()))
		_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(out.Fd()))
	}
	if in, err := openDevice("CONIN$"); err == nil {
		os.Stdin = in
		_ = windows.SetStdHandle(windows.STD_INPUT_HANDLE, windows.Handle(in.Fd()))
	}
}

func openDevice(name string) (*os.File, error) {
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), name), nil
}
