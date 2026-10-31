# bedrock-proxy

One executable for Pterodactyl/Pelican. Players connect over **RakNet on a single UDP port**; the proxy joins
your **NetherNet-only** Bedrock Dedicated Server on their behalf, so you never open BDS's per-player UDP
ports. It can also launch the server from a folder.

```
Players --RakNet, 1 UDP port--> bedrock-proxy --NetherNet, loopback--> BDS (online-mode=false)
```

## Setup (Pterodactyl / Pelican)

On this repository there's an egg for both pelican and pterodactyl. No need to do any other steps.

- `eggs/egg-vanilla-bedrock-proxy.json`: players are authenticated by the proxy and relayed (RakNet in front).
- `eggs/egg-vanilla-bedrock-mux.json`: mux mode (see below). Players join the server directly through one port.

## How it works and why it is safe enough

- The proxy authenticates players itself (Xbox login chain) on the public side.
- BDS with `online-mode=false` accepts a NetherNet peer carrying a self-signed identity token. Verified on
  BDS 1.26.52.3: with `online-mode=true` it rejects such a peer (error 37).
- Therefore **BDS must not be reachable except through the proxy.** Only the allocated public port may be
  published. Never expose the backend port (`backend_port`).

## Limits

- Players appear to BDS without an XUID, so XUID-based permissions/allowlists don't work; names do.
- One Bedrock protocol version at a time (the one gophertunnel supports); rebuild after Minecraft updates.
- Library versions are pinned in `go.mod`: a newer `go-nethernet` (v1.0.24) produced identities BDS rejected.
  Don't run `go get -u` without re-testing.
- Mojang is removing RakNet from clients (reported for 26.60), which will end the RakNet public side.
  Use `public_transport: "nethernet"` then.
- `BEDROCK_PROXY_INSECURE_TEST_NO_AUTH=1` disables player authentication. For automated tests only.

Credits: NetherNet single-port listener approach from Portal (Apache-2.0).

## Config placeholders (for panel eggs)

Any string in `config.json` may contain `{name}`, which the proxy replaces with the environment variable
`NAME` (Pterodactyl/Pelican expose egg variables that way). `{name:default}` uses the default when the
variable is unset or empty; without a default, a missing variable stops startup with a clear message.
Number fields accept `"10"` as well as `10`. `{port}` is reserved: it is the internal backend port and is
filled in at launch.

`config.example.json` uses this with the egg variables `EXECUTABLE`, `BACKEND_PORT`, `SERVERNAME` and
`MAX_PLAYERS`. `config.plain.example.json` is the same config with fixed values and needs no variables.

## Mux mode (one public port, players connect to the server directly)

`"public_transport": "mux"` does not terminate players at all. BDS opens one UDP port per player; the proxy
answers the NetherNet signaling for BDS, rewrites the answer so it advertises the single public UDP address,
and forwards each player's packets to the internal port BDS gave them (recognised by the ICE username in their
first STUN packet). Players talk to BDS end to end, so **BDS can keep `online-mode=true`** and sees the real
player, and nothing here depends on RakNet.

- Needs `backend_transport: "nethernet"` and BDS reachable from the proxy (default loopback).
- BDS needs a UDP port window, e.g. `"server-udp-ports": "19141-19240"` in `properties`: one port per
  concurrent player. These ports are internal and are never published.
- `public_ip` is optional. By default players are sent back to the host name or IP they connected with.
- Only TCP and UDP on the public port need to be open.
- In mux mode the log pairs each player's name and XUID with their address (`player joined` / `player left`).
  The address is what the proxy sees: on Docker Desktop for Windows that is Docker's gateway, not the player;
  on a Linux host it is normally the real IP.

## Environment mode (panel eggs, no config.json)

When `PROXY_COMMAND` is set, the proxy configures itself from environment variables for Bedrock Dedicated
Server behind the mux, and **ignores any `config.json`**, so what gets executed is decided by the egg and never
by a file a player can edit.

| Variable | Meaning | Default |
|---|---|---|
| `PROXY_COMMAND` | command that starts the server, e.g. `./bedrock_server` or `box64 ./bedrock_server` | (required, turns the mode on) |
| `SERVER_PORT` | the public port; the panel sets it | 19132 |
| `BACKEND_PORT` | the server's internal port | 19140 |
| `UDP_PORTS` | internal per-player UDP window | 19141-19240 |
| `ALLOW_LIST` | `true` or `false`; unset leaves the server's own setting alone | unset |

On every start it sets `server-port`, `online-mode=true`, `transport=nethernet`, `server-udp-ports` (and
`allow-list` if given) in `server.properties`. Values are validated before they are written.
