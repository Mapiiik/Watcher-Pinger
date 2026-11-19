//go:build windows

package main

import (
	"context"
	"embed"
	"fmt"
	"strings"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"watcher-pinger/src/status"
)

//go:embed icons/*.ico
var iconFiles embed.FS

// icons are those of the notification area by the level of the network, see icons/pinger.svg.
var icons = map[status.Level][]byte{
	status.Starting:  mustIcon("pinger"),
	status.AllUp:     mustIcon("all-up"),
	status.Losses:    mustIcon("losses"),
	status.Outage:    mustIcon("outage"),
	status.NoNetwork: mustIcon("no-network"),
}

func mustIcon(name string) []byte {
	icon, err := iconFiles.ReadFile("icons/" + name + ".ico")
	if err != nil {
		panic(err)
	}
	return icon
}

// texts of the icon and its menu, in English or in the language of Windows where there is a translation.
type texts struct {
	open, reload, quit          string
	starting, noNetwork         string
	answering, down, lossy, out string
	reloadFailed                string
}

var english = texts{
	open:         "Open the page",
	reload:       "Reload data",
	quit:         "Quit",
	starting:     "Pinging…",
	noNetwork:    "Cannot ping: no network",
	answering:    "Devices answering: %d of %d",
	down:         "Not answering: %d",
	lossy:        "Losing packets: %d",
	out:          "Access points out: %d",
	reloadFailed: "The data were not read, the old ones stay:",
}

var czech = texts{
	open:         "Otevřít stránku",
	reload:       "Načíst data znovu",
	quit:         "Ukončit",
	starting:     "Pinguji…",
	noNetwork:    "Nelze pingat: chybí síť",
	answering:    "Zařízení odpovídá: %d z %d",
	down:         "Neodpovídá: %d",
	lossy:        "Ztrácí pakety: %d",
	out:          "Přístupové body mimo provoz: %d",
	reloadFailed: "Data se nenačetla, zůstávají původní:",
}

// language picks the texts by the first language of the user's Windows that there is a translation for.
func language() texts {
	languages, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil {
		return english
	}
	for _, tag := range languages {
		switch strings.ToLower(strings.SplitN(tag, "-", 2)[0]) {
		case "cs":
			return czech
		case "en":
			return english
		}
	}
	return english
}

// run keeps the icon in the notification area: its colour and tooltip say how the network is, a click opens the
// page and its menu reads the data anew or quits. It returns once quit, or with the error the page stopped with.
func (d *desktop) run(ctx context.Context) error {
	t := language()
	result := make(chan error, 1)

	systray.Run(func() {
		systray.SetIcon(icons[status.Starting])
		systray.SetTooltip("Watcher Pinger\n" + t.starting)
		systray.SetOnTapped(d.openPage)
		open := systray.AddMenuItem(t.open, "")
		reload := systray.AddMenuItem(t.reload, "")
		systray.AddSeparator()
		quit := systray.AddMenuItem(t.quit, "")

		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			painted := painting{level: -1}
			for {
				select {
				case <-ctx.Done():
					systray.Quit()
					return
				case err := <-d.stopped:
					result <- err
					systray.Quit()
					return
				case <-quit.ClickedCh:
					systray.Quit()
					return
				case <-open.ClickedCh:
					d.openPage()
				case <-reload.ClickedCh:
					// the message waits for its OK, the icon does not
					go func() {
						if _, err := d.page.ReloadNow(ctx); err != nil {
							tell(t.reloadFailed + "\n" + err.Error())
						}
					}()
				case <-ticker.C:
				}
				painted = d.paint(t, painted)
			}
		}()
	}, nil)

	select {
	case err := <-result:
		return err
	default:
		return nil
	}
}

// painting is what the icon shows, so that it is set anew only once that changes.
type painting struct {
	level   status.Level
	tooltip string
}

func (d *desktop) paint(t texts, was painting) painting {
	states, _ := d.pings.Snapshot()
	summary := status.Of(d.page.Book(), states)
	now := painting{level: summary.Level, tooltip: tooltip(t, summary)}

	if now.level != was.level {
		systray.SetIcon(icons[now.level])
	}
	if now.tooltip != was.tooltip {
		systray.SetTooltip(now.tooltip)
	}
	return now
}

// tooltip sums the network up in a few lines, well within the 127 characters Windows shows.
func tooltip(t texts, s status.Summary) string {
	lines := []string{"Watcher Pinger"}
	switch s.Level {
	case status.Starting:
		lines = append(lines, t.starting)
	case status.NoNetwork:
		lines = append(lines, t.noNetwork)
	default:
		lines = append(lines, fmt.Sprintf(t.answering, s.Up, s.Devices))
		if s.AccessPointsOut > 0 {
			lines = append(lines, fmt.Sprintf(t.out, s.AccessPointsOut))
		}
		if s.Down > 0 {
			lines = append(lines, fmt.Sprintf(t.down, s.Down))
		}
		if s.Lossy > 0 {
			lines = append(lines, fmt.Sprintf(t.lossy, s.Lossy))
		}
	}
	return strings.Join(lines, "\n")
}
