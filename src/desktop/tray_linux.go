//go:build linux

package main

import (
	"embed"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
)

//go:embed icons/*.png
var iconFiles embed.FS

// iconData is an icon of the notification area, which a StatusNotifierItem takes as PNG.
func iconData(name string) []byte {
	icon, err := iconFiles.ReadFile("icons/" + name + ".png")
	if err != nil {
		panic(err)
	}
	return icon
}

// trayAvailable tells whether the desktop shows icons of programs (StatusNotifierItem): KDE, Xfce, Cinnamon and the
// like do, GNOME with the AppIndicator extension, which Ubuntu turns on; a session over SSH has none.
func trayAvailable() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	var owned bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&owned)
	return err == nil && owned
}

// language picks the texts by the language of the session: LANGUAGE, a list, then LC_ALL, LC_MESSAGES and LANG.
func language() texts {
	var tags []string
	tags = append(tags, strings.Split(os.Getenv("LANGUAGE"), ":")...)
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		tags = append(tags, os.Getenv(key))
	}
	for _, tag := range tags {
		if t, ok := translation(tag); ok {
			return t
		}
	}
	return english
}
