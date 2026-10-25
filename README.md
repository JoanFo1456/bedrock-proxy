# bedrock-proxy

One executable for Pterodactyl/Pelican. Players connect over **RakNet on a single UDP port**; the proxy joins
your **NetherNet-only** Bedrock Dedicated Server on their behalf, so you never open BDS's per-player UDP
ports. It can also launch the server from a folder.

```
Players --RakNet, 1 UDP port--> bedrock-proxy --NetherNet, loopback--> BDS (online-mode=false)
```

## Setup (Pterodactyl / Pelican)

1. Put `bedrock-proxy-linux` next to `bedrock_server` and make it executable (`0755`).
2. Create `config.json` there:

```json
{
  "public_transport": "raknet",
  "backend_transport": "nethernet",
  "server_dir": ".",
  "server_command": ["./bedrock_server"],
  "backend_port": 19140,
  "motd": "My Server",
  "max_players": 10
}
```

3. In `server.properties` set `server-port=19140`, `online-mode=false`, `enable-lan-visibility=false`
   (and `allow-list=false` unless you maintain `allowlist.json`). Keep `transport=nethernet`.
4. Startup command: `LD_LIBRARY_PATH=. ./bedrock-proxy-linux`
5. Players join the panel's allocated port.

If the server user may write `server.properties`, you can instead list these under `"properties"` in
`config.json` and the proxy sets them on every start (`{port}` = backend port).

Other servers: `backend_transport: "raknet"` for PocketMine, Dragonfly, Nukkit and similar.
`public_transport: "nethernet"` serves players over NetherNet (TCP signaling + WebRTC UDP, same port number).

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
