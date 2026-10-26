# bedrock-proxy

One executable for Pterodactyl/Pelican. Players connect over **RakNet on a single UDP port**; the proxy joins
your **NetherNet-only** Bedrock Dedicated Server on their behalf, so you never open BDS's per-player UDP
ports. It can also launch the server from a folder.

```
Players --RakNet, 1 UDP port--> bedrock-proxy --NetherNet, loopback--> BDS (online-mode=false)
```

## Setup (Pterodactyl / Pelican)

On this repository there's an egg for both pelican and pterodactyl. No need to do any other steps.

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
