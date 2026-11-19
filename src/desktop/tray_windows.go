//go:build windows

package main

import (
	"embed"

	"golang.org/x/sys/windows"
)

//go:embed icons/*.ico
var iconFiles embed.FS

// iconData is an icon of the notification area, which Windows takes as .ico.
func iconData(name string) []byte {
	icon, err := iconFiles.ReadFile("icons/" + name + ".ico")
	if err != nil {
		panic(err)
	}
	return icon
}

// trayAvailable: Windows always has its notification area.
func trayAvailable() bool {
	return true
}

// language picks the texts by the first language of the user's Windows that there is a translation for.
func language() texts {
	languages, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil {
		return english
	}
	for _, tag := range languages {
		if t, ok := translation(tag); ok {
			return t
		}
	}
	return english
}
