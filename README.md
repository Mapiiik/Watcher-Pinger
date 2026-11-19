# Watcher Pinger

[![CI](https://github.com/Mapiiik/Watcher-Pinger/actions/workflows/ci.yml/badge.svg)](https://github.com/Mapiiik/Watcher-Pinger/actions/workflows/ci.yml)
[![License: AGPL v3+](https://img.shields.io/badge/License-AGPL%20v3%2B-blue.svg)](LICENSE.md)
[![Go](https://img.shields.io/badge/Go-1.27-00ADD8.svg)](go.mod)

Pings the network [Watcher NMS](https://github.com/Mapiiik/Watcher-NMS) knows and shows it as a tree in the browser:
the access points with their devices and customers, with what answers, what does not, and where an outage starts.

It runs from a file Watcher NMS hands out, so it keeps working in a part of the network cut off from the rest — the
technician standing in it has the last copy on their own computer. The same page runs [on a server](#on-a-server)
too, for everyone who signs in to watch the network online.

## Using it

1. Download `watcher-pinger.exe` from the [releases](https://github.com/Mapiiik/Watcher-Pinger/releases) (`watcher-pinger`
   for Linux, see below).
2. In Watcher NMS, **Access Points → Pinger → Download (.json)** gives `watcher-pinger.json`; put it next to
   `watcher-pinger.exe` (a synchronised folder kept available offline does well).
3. Run `watcher-pinger.exe`. The page opens in the default browser, and an icon in the notification area says how
   the network is: a click opens the page again, its menu reads the data anew or quits the pinger.

| Icon | |
| --- | --- |
| blue | nothing heard from a device yet, and the icon of the program |
| green | the devices of the network answer |
| orange | a device does not answer, or answers but loses more than 20 % of its last pings |
| white with a red edge | an access point is out: none of its devices answers |
| grey | no ping goes out, the pinger itself is cut off |

Only the devices of the network count, as a router switched off at home is no fault of the network; its tooltip
counts them. The outage stands apart by more than its colour, for the colour-blind and from the red of Watcher CRM.

On Linux, `watcher-pinger` from the same releases shows the same icon where the desktop shows icons of programs
(StatusNotifierItem: KDE, Xfce, Cinnamon, GNOME with the AppIndicator extension, which Ubuntu has on), and keeps to
the console where it does not, over SSH say, stopping on Ctrl+C. It pings over an unprivileged ICMP socket, which most
distributions allow in `net.ipv4.ping_group_range`; where they do not, `sudo setcap cap_net_raw+ep watcher-pinger`
gives it a raw one.

The page has two tabs over the same tree of access points: **Network** with their devices, and **Customers** by the
access point they are connected through. Both say where the network is down, so that a customer behind an outage is
told apart from one whose router is off. The search covers both.

The page is in English, or in the first language of the browser's preferences it has a translation for (Czech so
far).

Once the page loses the pinger — quit from its icon, say — the page is covered with a notice that what it shows is not
current. The port stays the same between runs, so a pinger started again is found by the open page on its own, and
starting it while it runs just opens its page.

A newer file from Watcher NMS is taken by **Reload data** on the page, without starting the pinger again: an address
kept keeps its state and the loss over its last pings, and what was expanded stays expanded. A file that cannot be
read changes nothing, and the page says why.

| Option | Default | |
| --- | --- | --- |
| `-data` | `watcher-pinger.json` next to the program | the file from Watcher NMS |
| `-listen` | `127.0.0.1:47380` | where the page is served |
| `-no-browser` | off | do not open the page |
| `-console` | off | keep to the console instead of the icon: the terminal it is started from, where Ctrl+C stops it, or on Windows a console window of its own where it is started from Explorer |
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
(server-sent events) and keeps in the page only what is expanded. Every page open watches what it shows.

## On a server

`watcher-pinger-server` is the same pinger and page for Linux, as the image `ghcr.io/mapiiik/watcher-pinger`
(`latest` from `main`, the version and its major number from a tag). It pings over an unprivileged ICMP socket where
the system allows it in `net.ipv4.ping_group_range`, as Docker does, and over a raw one otherwise, which needs
`NET_RAW`; `PINGER_ICMP` picks one for good. An unprivileged socket gets only its own replies, while a raw one sees
those of every program on the machine and passes over what is not its own. It is set up from the environment; [`.env.example`](.env.example) lists every variable with what
it is for, and [`compose.production.yaml`](compose.production.yaml) runs it.

- **Listening** is that of [Watcher Agent](https://github.com/Mapiiik/Watcher-Agent): with `PINGER_HOSTNAME` a
  certificate from Let's Encrypt over TLS-ALPN on 443, without it a self-signed one, plain HTTP redirecting to HTTPS.
  Behind a proxy passing TLS through, HAProxy in TCP mode say, `PINGER_USE_PROXY_PROTOCOL` takes the PROXY header
  from the networks in `PINGER_PROXY_ALLOW_CIDRS`. An empty `PINGER_LISTEN_HTTPS` serves the page on
  `PINGER_LISTEN_HTTP` alone, for TLS ended by a proxy in front.
- **Who may see it** is `PINGER_AUTH`, which has no default:
  - `oidc` signs in at the identity provider of the organisation (Authentik, Keycloak, …) with the authorization code
    flow and PKCE, the callback being `https://<PINGER_HOSTNAME>/auth/callback`. The session is a signed cookie, so
    the server keeps nothing and may start again; `PINGER_OIDC_GROUPS` lets in members of the groups given only.
  - `header` takes the user from a header set by a proxy doing its own sign-in, forward auth say, and only from the
    proxies in `PINGER_AUTH_PROXY_CIDRS`.
  - `none` asks nobody, for a server guarded some other way; it says so in its log.

  A sign-in not set up fully stops the start rather than letting anybody in.
- **The network** comes from [Watcher NMS](https://github.com/Mapiiik/Watcher-NMS) at
  `/api/access-points/pinger.json`, fetched with the key of a user of the role `watcher-pinger` every
  `PINGER_NMS_INTERVAL` (an hour) and on **Reload data**. The last network fetched is kept in `PINGER_DATA`, a
  volume, so that a start with Watcher NMS out of reach pings what was pinged before. Without `PINGER_NMS_URL` the
  server reads `PINGER_DATA` alone.

Where it runs decides what it sees: the server pings from its place in the network, so it needs routes into all of
it and should not stand behind anything it watches.

## Building

The CI lints and tests every push, the ICMP helper on Windows itself and both ICMP sockets with the right to them, keeps
the program it builds as an artifact of the run and pushes the image of the server. A tag, a bare version as the
other Watcher applications have it (`1.0.0`, `1.1.0-RC1` for a prerelease), publishes the program as a release.

There is no need for Go on the machine, a container does:

```sh
docker run --rm -v "$PWD:/src" -w /src -e GOOS=windows -e GOARCH=amd64 golang:1.27 \
    go build -trimpath -ldflags "-s -w" -o dist/watcher-pinger.exe ./src/desktop
```

Tests run in the container too; those of the ICMP helper only on Windows, built with
`GOOS=windows go test -c -o dist/icmpwin.test.exe ./src/infra/icmpwin` and run there.

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go test -race ./...
docker run --rm -v "$PWD:/src" -w /src golangci/golangci-lint:v2.15.0 golangci-lint run ./...
```

The icons are drawn in `src/desktop/icons/*.svg` and made into `.ico` and `.png` files by `make.sh` there, with
ImageMagick and librsvg. The program for Windows is a console one whose manifest
([`src/desktop/winres/app.manifest`](src/desktop/winres/app.manifest)) asks for no console when it is started from
Explorer, so that it is only the icon there, while cmd or PowerShell wait for it, show what it writes and stop it on
Ctrl+C. Windows older than 11 24H2 do not know the setting and give it a console anyway, which it closes at once: a
window flashes at the start, nothing else. The manifest and the icon of the program go into a Windows resource beside
the code:

```sh
cd src/desktop && go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --arch amd64 --out rsrc
```

The image of the server:

```sh
docker build -f docker/Dockerfile.production --build-arg VERSION=$(git describe --tags --always) -t watcher-pinger .
```

## License

[GNU Affero General Public License v3.0 or later](LICENSE.md).
