// Package status sums the network up in one word, for the icon of the pinger: whether its devices answer, whether
// some lose packets or do not answer, or whether an access point is out. The customers are left out, as a router
// switched off at home is no fault of the network.
package status

import (
	"watcher-pinger/src/addressbook"
	"watcher-pinger/src/monitor"
)

// Level is the word the network is summed up in.
type Level int

const (
	// Starting has not heard from any device yet.
	Starting Level = iota
	// AllUp has every device pinged answering, without losing much.
	AllUp
	// Losses has a device that does not answer, or one that answers but loses much.
	Losses
	// Outage has an access point out: all of its devices do not answer, as the page says where an outage starts.
	Outage
	// NoNetwork could send no ping at all, the pinger itself being cut off.
	NoNetwork
)

// HighLoss is the share of the last pings, in per cent, that a device answering may lose without it counting among
// the losses; the page shows such a device among the outages too. One lost ping of twenty is 5 %.
const HighLoss = 20

// Summary counts the devices of the network by their state, a device listed twice counted twice.
type Summary struct {
	Level           Level
	Devices         int
	Up              int
	Down            int
	Lossy           int
	Failed          int
	AccessPointsOut int
}

// Of sums up the network of the book by the states of its addresses. The devices without an access point count as
// well, only no access point can be out for them.
func Of(book *addressbook.Book, states map[string]monitor.State) Summary {
	var s Summary
	for _, ap := range book.AccessPoints {
		devices, down := s.count(ap.Hosts, states)
		if devices > 0 && down == devices {
			s.AccessPointsOut++
		}
	}
	s.count(book.Unassigned, states)

	switch {
	case s.Up == 0 && s.Down == 0 && s.Failed > 0:
		s.Level = NoNetwork
	case s.Up == 0 && s.Down == 0:
		s.Level = Starting
	case s.AccessPointsOut > 0:
		s.Level = Outage
	case s.Down > 0 || s.Lossy > 0:
		s.Level = Losses
	default:
		s.Level = AllUp
	}
	return s
}

// count adds the devices among the hosts to the summary and tells how many there were and how many of them do not
// answer.
func (s *Summary) count(hosts []addressbook.Host, states map[string]monitor.State) (devices, down int) {
	for _, host := range hosts {
		if host.Kind == addressbook.KindCustomer {
			continue
		}
		devices++
		state := states[host.IP]
		switch state.Status {
		case monitor.Up:
			s.Up++
			if state.Sent > 0 && (state.Sent-state.Received)*100 > HighLoss*state.Sent {
				s.Lossy++
			}
		case monitor.Down:
			s.Down++
			down++
		case monitor.Failed:
			s.Failed++
		}
	}
	s.Devices += devices
	return devices, down
}
