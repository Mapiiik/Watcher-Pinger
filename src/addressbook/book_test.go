package addressbook

import (
	"reflect"
	"strings"
	"testing"
)

const sample = `{
	"format": "watcher-pinger",
	"version": 1,
	"generated": "2026-11-19T16:00:00+01:00",
	"accessPoints": [
		{"id": "root", "name": "Root", "parentId": null, "hosts": [
			{"name": "Root Router", "ip": "10.9.0.1", "kind": "routeros-device", "contact": false}
		]},
		{"id": "child", "name": "Child", "parentId": "root", "hosts": [
			{"name": "Child - Root - IN", "ip": "10.9.1.243", "kind": "radio-unit", "manufacturer": "ACME Radio", "contact": false},
			{"name": "Landlord (C-1)", "ip": "192.0.2.21", "kind": "customer", "manufacturer": null, "contact": true},
			{"name": "Again", "ip": "10.9.0.1", "kind": "routeros-device", "contact": false}
		]}
	],
	"unassigned": [
		{"name": "Nowhere (C-2)", "ip": "2001:db8::27", "kind": "customer", "contact": false}
	]
}`

func TestABookIsRead(t *testing.T) {
	book, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got := len(book.AccessPoints); got != 2 {
		t.Fatalf("%d access points, want 2", got)
	}
	child := book.AccessPoints[1]
	if child.ParentID == nil || *child.ParentID != "root" {
		t.Errorf("parent of the child is %v, want root", child.ParentID)
	}
	if want := (Host{Name: "Landlord (C-1)", IP: "192.0.2.21", Kind: KindCustomer, Contact: true}); child.Hosts[1] != want {
		t.Errorf("host %+v, want %+v", child.Hosts[1], want)
	}
	if got := child.Hosts[0].Manufacturer; got != "ACME Radio" {
		t.Errorf("manufacturer %q, want ACME Radio", got)
	}
	if got := book.Generated.UTC().Format("2006-01-02 15:04"); got != "2026-11-19 15:00" {
		t.Errorf("generated %s", got)
	}
}

func TestEveryAddressIsListedOnce(t *testing.T) {
	book, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := []string{"10.9.0.1", "10.9.1.243", "192.0.2.21", "2001:db8::27"}
	if got := book.IPs(); !reflect.DeepEqual(got, want) {
		t.Errorf("IPs() = %v, want %v", got, want)
	}
	wantDevices := []string{"10.9.0.1", "10.9.1.243"}
	if got := book.DeviceIPs(); !reflect.DeepEqual(got, wantDevices) {
		t.Errorf("DeviceIPs() = %v, want %v", got, wantDevices)
	}
}

func TestWhatDoesNotHoldTogetherIsRefused(t *testing.T) {
	cases := map[string]string{
		"not JSON":          `not json`,
		"another format":    strings.Replace(sample, `"watcher-pinger"`, `"something-else"`, 1),
		"a newer version":   strings.Replace(sample, `"version": 1`, `"version": 2`, 1),
		"a parent later":    strings.Replace(sample, `"parentId": "root"`, `"parentId": "later"`, 1),
		"a repeated id":     strings.Replace(sample, `"id": "child"`, `"id": "root"`, 1),
		"not an IP address": strings.Replace(sample, `"10.9.1.243"`, `"10.9.1"`, 1),
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(content)); err == nil {
				t.Error("want an error")
			}
		})
	}
}
