//go:build windows

package main

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The program is a console one whose manifest asks Windows for no console when it is started from Explorer
// (winres/app.manifest): there it is only the icon in the notification area, while cmd or PowerShell wait for it and
// show what it writes, as for any command.

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procAllocConsole          = kernel32.NewProc("AllocConsole")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
	procGetConsoleWindow      = kernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
)

// console tells whether the program writes to a console, and ownConsole whether that console is its own, opened
// for it and closing with it.
var console, ownConsole bool

// setUpConsole keeps the console of the terminal the program was started from, and gives it one of its own where it
// has none and is asked to keep to the console. A Windows older than 11 24H2 gives the program started from Explorer
// a console of its own anyway, which it closes unless asked to keep to it.
func setUpConsole(keep bool) {
	has := hasConsole()
	if has && alone() {
		if !keep {
			_, _, _ = procFreeConsole.Call()
			return
		}
		ownConsole = true
	}
	if !has && keep {
		if r, _, _ := procAllocConsole.Call(); r == 0 {
			return
		}
		has, ownConsole = true, true
		reopenStandardHandles()
	}
	console = has
}

func hasConsole() bool {
	r, _, _ := procGetConsoleWindow.Call()
	return r != 0
}

// alone tells whether the console has no other process but this one, that is, was made for it.
func alone() bool {
	var ids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	return n == 1
}

// reopenStandardHandles points the standard handles at a console opened later than the program started.
func reopenStandardHandles() {
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
