//go:build windows || linux

package main

import (
	"strings"
	"testing"

	"watcher-pinger/src/status"
)

func TestTheLanguageOfTheSystemPicksTheTexts(t *testing.T) {
	for tag, want := range map[string]string{
		"cs-CZ":       czech.quit,
		"cs_CZ.UTF-8": czech.quit,
		"cs":          czech.quit,
		"en-GB":       english.quit,
		"en_US.UTF-8": english.quit,
	} {
		got, ok := translation(tag)
		if !ok || got.quit != want {
			t.Errorf("%q: %q, %v", tag, got.quit, ok)
		}
	}
	for _, tag := range []string{"", "C", "POSIX", "de_DE.UTF-8", "sk-SK"} {
		if _, ok := translation(tag); ok {
			t.Errorf("%q has a translation", tag)
		}
	}
}

func TestEveryLevelHasItsIcon(t *testing.T) {
	for _, level := range []status.Level{status.Starting, status.AllUp, status.Losses, status.Outage, status.NoNetwork} {
		if len(icons[level]) == 0 {
			t.Errorf("level %d has no icon", level)
		}
	}
}

// The tooltip counts the devices, and says what is wrong only where something is.
func TestTheTooltipSumsTheNetworkUp(t *testing.T) {
	quiet := tooltip(english, status.Summary{Level: status.AllUp, Devices: 3, Up: 3})
	if quiet != "Watcher Pinger\nDevices answering: 3 of 3" {
		t.Errorf("all up: %q", quiet)
	}

	out := tooltip(czech, status.Summary{Level: status.Outage, Devices: 4, Up: 1, Down: 2, Lossy: 1, Failed: 1,
		AccessPointsOut: 1})
	for _, want := range []string{"1 z 4", "mimo provoz: 1", "Neodpovídá: 2", "Ztrácí pakety: 1", "Nelze pingnout: 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("outage: %q not in %q", want, out)
		}
	}
	if len([]rune(out)) > 127 {
		t.Errorf("%d characters, Windows shows 127", len([]rune(out)))
	}
}
