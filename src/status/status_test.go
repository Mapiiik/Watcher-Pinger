package status

import (
	"testing"

	"watcher-pinger/src/addressbook"
	"watcher-pinger/src/monitor"
)

// network: a root with a router and a power supply, a child with a radio unit, and customers on both.
func network() *addressbook.Book {
	root := "root"
	return &addressbook.Book{AccessPoints: []addressbook.AccessPoint{
		{ID: root, Name: "Root", Hosts: []addressbook.Host{
			{Name: "Router", IP: "10.0.0.1", Kind: addressbook.KindRouterosDevice},
			{Name: "Power", IP: "10.0.0.2", Kind: addressbook.KindPowerSupply},
			{Name: "Doe Jane", IP: "192.0.2.1", Kind: addressbook.KindCustomer},
		}},
		{ID: "child", Name: "Child", ParentID: &root, Hosts: []addressbook.Host{
			{Name: "Child-1", IP: "10.0.1.32", Kind: addressbook.KindRadioUnit},
			{Name: "Roe John", IP: "192.0.2.2", Kind: addressbook.KindCustomer},
		}},
	}}
}

func up(sent, received int) monitor.State {
	return monitor.State{Status: monitor.Up, Sent: sent, Received: received}
}

var down = monitor.State{Status: monitor.Down, Sent: 3}

func TestTheNetworkInAWord(t *testing.T) {
	allUp := map[string]monitor.State{"10.0.0.1": up(20, 20), "10.0.0.2": up(20, 20), "10.0.1.32": up(20, 20)}
	with := func(ip string, state monitor.State) map[string]monitor.State {
		states := map[string]monitor.State{}
		for k, v := range allUp {
			states[k] = v
		}
		states[ip] = state
		return states
	}

	for name, tc := range map[string]struct {
		states map[string]monitor.State
		want   Level
	}{
		"nothing pinged yet":            {map[string]monitor.State{}, Starting},
		"every device answers":          {allUp, AllUp},
		"a lost ping of twenty":         {with("10.0.0.1", up(20, 19)), AllUp},
		"a fifth lost is not yet much":  {with("10.0.0.1", up(20, 16)), AllUp},
		"a quarter lost":                {with("10.0.0.1", up(20, 15)), Losses},
		"a device down":                 {with("10.0.0.2", down), Losses},
		"every device of a point down":  {with("10.0.1.32", down), Outage},
		"customers do not count":        {with("192.0.2.1", down), AllUp},
		"some pinged, the rest not yet": {map[string]monitor.State{"10.0.0.1": up(1, 1)}, AllUp},
		"no ping went out": {map[string]monitor.State{
			"10.0.0.1": {Status: monitor.Failed}, "10.0.0.2": {Status: monitor.Failed},
		}, NoNetwork},
	} {
		if got := Of(network(), tc.states).Level; got != tc.want {
			t.Errorf("%s: level %d, want %d", name, got, tc.want)
		}
	}
}

func TestTheDevicesAreCounted(t *testing.T) {
	got := Of(network(), map[string]monitor.State{
		"10.0.0.1": up(20, 10), "10.0.0.2": down, "10.0.1.32": down, "192.0.2.1": down,
	})
	want := Summary{Level: Outage, Devices: 3, Up: 1, Down: 2, Lossy: 1, AccessPointsOut: 1}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A device without an access point counts like any other, it only puts no access point out.
func TestDevicesWithoutAnAccessPointCount(t *testing.T) {
	book := network()
	book.Unassigned = []addressbook.Host{
		{Name: "Lost", IP: "10.0.9.1", Kind: addressbook.KindRouterosDevice},
		{Name: "Moe Ann", IP: "192.0.2.9", Kind: addressbook.KindCustomer},
	}
	states := map[string]monitor.State{
		"10.0.0.1": up(5, 5), "10.0.0.2": up(5, 5), "10.0.1.32": up(5, 5), "10.0.9.1": down,
	}

	s := Of(book, states)
	if s.Devices != 4 || s.Up != 3 || s.Down != 1 || s.AccessPointsOut != 0 || s.Level != Losses {
		t.Errorf("%+v", s)
	}
}
