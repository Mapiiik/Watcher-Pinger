// Package addressbook reads what Watcher NMS hands out to be pinged: the tree of access points with their addresses.
package addressbook

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// Format names the file, Version is the newest one this reader understands.
const (
	Format  = "watcher-pinger"
	Version = 1
)

// Kind says what an address belongs to.
type Kind string

const (
	KindRadioUnit      Kind = "radio-unit"
	KindRouterosDevice Kind = "routeros-device"
	KindPowerSupply    Kind = "power-supply"
	KindCustomer       Kind = "customer"
)

// Host is one address and what it is shown as. Manufacturer is empty where it is not known. Contact marks a customer
// who is a contact of an access point, the landlord of a mast as a rule.
type Host struct {
	Name         string `json:"name"`
	IP           string `json:"ip"`
	Kind         Kind   `json:"kind"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Contact      bool   `json:"contact"`
}

// AccessPoint names its parent, nil for a root; a parent comes before its children.
type AccessPoint struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	ParentID *string `json:"parentId"`
	Hosts    []Host  `json:"hosts"`
}

// Book is the whole file.
type Book struct {
	Format       string        `json:"format"`
	Version      int           `json:"version"`
	Generated    time.Time     `json:"generated"`
	AccessPoints []AccessPoint `json:"accessPoints"`
	Unassigned   []Host        `json:"unassigned"`
}

// Load reads the book from a file.
func Load(path string) (*Book, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	return Parse(f)
}

// Parse reads the book and refuses one that is not a book, is newer than this reader, or does not hold together.
func Parse(r io.Reader) (*Book, error) {
	var book Book
	if err := json.NewDecoder(r).Decode(&book); err != nil {
		return nil, fmt.Errorf("not a readable file: %w", err)
	}
	if book.Format != Format {
		return nil, fmt.Errorf("not a %s file", Format)
	}
	if book.Version < 1 || book.Version > Version {
		return nil, fmt.Errorf("version %d, this program reads up to %d", book.Version, Version)
	}

	seen := make(map[string]bool, len(book.AccessPoints))
	for _, ap := range book.AccessPoints {
		if ap.ID == "" || seen[ap.ID] {
			return nil, fmt.Errorf("access point %q: missing or repeated id", ap.Name)
		}
		if ap.ParentID != nil && !seen[*ap.ParentID] {
			return nil, fmt.Errorf("access point %q: its parent is not listed before it", ap.Name)
		}
		seen[ap.ID] = true

		if err := checkHosts(ap.Hosts); err != nil {
			return nil, fmt.Errorf("access point %q: %w", ap.Name, err)
		}
	}
	if err := checkHosts(book.Unassigned); err != nil {
		return nil, err
	}

	return &book, nil
}

func checkHosts(hosts []Host) error {
	for _, host := range hosts {
		if net.ParseIP(host.IP) == nil {
			return fmt.Errorf("host %q: %q is not an IP address", host.Name, host.IP)
		}
	}
	return nil
}

// IPs lists every address once.
func (b *Book) IPs() []string {
	return b.collect(func(Host) bool { return true })
}

// DeviceIPs lists the addresses of the devices of the network once, every one but those of customers.
func (b *Book) DeviceIPs() []string {
	return b.collect(func(host Host) bool { return host.Kind != KindCustomer })
}

func (b *Book) collect(wanted func(Host) bool) []string {
	seen := make(map[string]bool)
	var ips []string

	add := func(hosts []Host) {
		for _, host := range hosts {
			if wanted(host) && !seen[host.IP] {
				seen[host.IP] = true
				ips = append(ips, host.IP)
			}
		}
	}
	for _, ap := range b.AccessPoints {
		add(ap.Hosts)
	}
	add(b.Unassigned)

	return ips
}
