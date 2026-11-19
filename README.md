# Watcher Pinger

[![CI](https://github.com/Mapiiik/Watcher-Pinger/actions/workflows/ci.yml/badge.svg)](https://github.com/Mapiiik/Watcher-Pinger/actions/workflows/ci.yml)
[![License: AGPL v3+](https://img.shields.io/badge/License-AGPL%20v3%2B-blue.svg)](LICENSE.md)
[![Go](https://img.shields.io/badge/Go-1.27-00ADD8.svg)](go.mod)

Pings the network [Watcher NMS](https://github.com/Mapiiik/Watcher-NMS) knows and shows it as a tree in the browser:
the access points with their devices and customers, with what answers, what does not, and where an outage starts.

It runs from a file Watcher NMS hands out, so it keeps working in a part of the network cut off from the rest — the
technician standing in it has the last copy on their own computer.

## Using it

1. Download `watcher-pinger.exe` from the [releases](https://github.com/Mapiiik/Watcher-Pinger/releases).
2. In Watcher NMS, **Access Points → Pinger → Download (.json)** gives `watcher-pinger.json`; put it next to
   `watcher-pinger.exe` (a synchronised folder kept available offline does well).
3. Run `watcher-pinger.exe`. The page opens in the default browser; closing the console window stops the pinger.

The page has two tabs over the same tree of access points: **Network** with their devices, and **Customers** by the
access point they are connected through. Both say where the network is down, so that a customer behind an outage is
told apart from one whose router is off. The search covers both.

The page is in English, or in the first language of the browser's preferences it has a translation for (Czech so
far).

Once the page loses the pinger — its window closed, say — the page is covered with a notice that what it shows is not
current. The port stays the same between runs, so a pinger started again is found by the open page on its own, and
starting it while it runs just opens its page.

| Option | Default | |
| --- | --- | --- |
| `-data` | `watcher-pinger.json` next to the program | the file from Watcher NMS |
| `-listen` | `127.0.0.1:47380` | where the page is served |
| `-no-browser` | off | do not open the page |
| `-timeout` | `1s` | how long to wait for a reply |
| `-watched-interval` | `5s` | how often what is expanded on the page is pinged |
| `-device-interval` | `30s` | how often the other devices of the network are pinged |
| `-idle-interval` | `1m` | how often the other customers are pinged |
| `-version` | | tell the version and stop |

## How it pings

Through the ICMP helper of Windows (`IcmpSendEcho2` in `iphlpapi.dll`), which needs no administrator, unlike a raw
socket. The requests go out from a few threads of the pinger's own, each taking the replies to its requests as APCs
while it waits; a thread waiting for every request makes Windows wake them all whenever any one is done, which costs
the processor ten times as much. Sending a ping takes a few milliseconds of the processor on some machines, which is
what the intervals are set by.

At the start every address is pinged at once, which takes a few seconds; the pings after that are spread evenly over
their intervals. What is expanded on the page is pinged every 5 seconds, the other devices every 30 and the other
customers every 60. An address that answered before is down once it misses two pings in a row; an access point whose
devices are all down is where an outage starts, and everything below it is behind the outage. Over the last 20 pings
of each address the page shows the loss beside the round trip, and the average and the longest round trip in the
tooltip of its row.

The tree can be narrowed to the **Outages**, among them the addresses that answer but lose more than 20 %, or to the
**Losses**, the addresses that answer but lose anything at all, which is where a poor link shows.

The page is served on the loopback only and answers no other host name. It receives the states as they change
(server-sent events) and keeps in the page only what is expanded.

## Building

The CI lints and tests every push, the ICMP helper on Windows itself, and keeps the program it builds as an
artifact of the run. A tag, a bare version as the other Watcher applications have it (`1.0.0`, `1.1.0-RC1` for a
prerelease), publishes the program as a release.

There is no need for Go on the machine, a container does:

```sh
docker run --rm -v "$PWD:/src" -w /src -e GOOS=windows -e GOARCH=amd64 golang:1.27 \
    go build -trimpath -ldflags "-s -w" -o dist/watcher-pinger.exe ./src/main
```

Tests run in the container too; those of the ICMP helper only on Windows, built with
`GOOS=windows go test -c -o dist/icmpwin.test.exe ./src/infra/icmpwin` and run there.

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go test -race ./...
docker run --rm -v "$PWD:/src" -w /src golangci/golangci-lint:v2.15.0 golangci-lint run ./...
```

## License

[GNU Affero General Public License v3.0 or later](LICENSE.md).
