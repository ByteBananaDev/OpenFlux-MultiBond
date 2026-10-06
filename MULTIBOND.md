# OpenFlux MultiBond

Experimental adaptive multi-carrier fork of OpenFlux.

Base upstream: `p1neappleXpress/OpenFlux`.

## v0.1 status

Phase 1 is integrated into the real OpenFlux `Session` path.

Implemented:

- up to **256** registered physical channels;
- default target: **196 active** channels;
- remaining connected channels stay in reserve;
- failed/disconnected channels leave the active pool automatically;
- averaged RTT using EWMA instead of reacting to one ping spike;
- preferred RTT: **<= 120 ms**;
- normal RTT ceiling: **150 ms**;
- emergency ceiling: **300 ms**, used only to preserve minimum connectivity;
- throughput weighted more heavily than latency;
- reconnect penalty and send-failure EWMA in the score;
- weighted rendezvous hashing plus a flow-pin table so established IP flows stay on one carrier while it remains ACTIVE;
- bounded background RTT probing: 8 carriers/second;
- runtime channel add/remove support;
- transport agnostic: Yandex legacy, Volga, Boards, Mail.ru, Cups, MAX/WebRTC and Direct can share the pool;
- scheduler snapshot API for future status UI/API.

Default score:

- throughput: 50%;
- averaged RTT: 30%;
- send-failure EWMA: 15%;
- reconnect/stability: 5%.

## RTT policy

A single slow ping does not immediately disable a carrier.

Default policy:

```text
0..120 ms     preferred
120..150 ms   allowed, progressively lower score
>150 ms       normal demotion after BadRTTHold
<=300 ms      emergency-only if the healthy pool collapses
```

A recovered channel must remain healthy for `RecoveryHold` before returning
to normal service. The recovery delay applies only after the channel actually
spent `BadRTTHold` above the normal RTT ceiling.

## Running MultiBond

MultiBond requires the negotiated encrypted Session path, therefore use the
same encryption secret and session context on both peers.

CLI:

```bash
openflux \
  --role=client \
  --config=client.conf \
  --bond \
  --bond-max=256 \
  --bond-active=196 \
  --bond-preferred-rtt=120ms \
  --bond-max-rtt=150ms
```

The same settings can live in `[Interface]`:

```ini
[Interface]
Role = client
Inbound = socks5
EncryptionKeyFile = secret.txt
SessionContext = multibond-v01

Bond = true
BondMax = 256
BondActive = 196
BondMinActive = 4
BondPreferredRTT = 120ms
BondMaxRTT = 150ms
BondEmergencyRTT = 300ms
BondRTTSpread = 30ms

[Transport "volga-01"]
Type = vyandex
Priority = 50
URL = YOUR_VOLGA_DOCUMENT_01

[Transport "volga-02"]
Type = vyandex
Priority = 50
URL = YOUR_VOLGA_DOCUMENT_02

[Transport "mail-01"]
Type = mailru
Priority = 50
URL = YOUR_MAILRU_DOCUMENT

[Transport "max-01"]
Type = oneme
Priority = 50
Token = YOUR_MAX_TOKEN
UID = YOUR_MAX_UID
```

Use unique transport names. The client and exit must describe corresponding
carriers consistently. Repeat `[Transport "..."]` sections as needed up to
the configured channel limit.

## Active / reserve behavior

Every second MultiBond refreshes transport state and recomputes the pool.

```text
256 registered
      |
      +-- healthy + best score --> ACTIVE (target 196)
      |
      +-- healthy but lower score --> RESERVE
      |
      +-- disconnected/stalled --> FAILED
```

When an active carrier disappears, eligible reserve carriers can replace it.
A reserve channel only replaces a healthy active channel when its score is at
least 10% better by default, which prevents constant pool flapping.

Established flows are pinned to their selected carrier while it remains ACTIVE.
If that carrier fails or is demoted (for example after sustained RTT above the
configured ceiling), the flow is repinned to the current active pool. Idle pins
expire after five minutes.

RTT probing is deliberately bounded rather than pinging all 256 channels every
second. Pool changes are logged immediately; top active/reserve channel metrics
are logged periodically at debug level 2.

## Important phase-1 limitation

v0.1 balances **whole flows** across channels. It does not yet split packets
from one TCP connection across multiple carriers.

That is intentional. Packet striping without receive-side ordering can create
large reordering and make TCP slower even when aggregate carrier bandwidth is
higher.

Phase 2 will add:

- per-flow stripe groups;
- sequence-aware framing;
- receive-side reorder buffer;
- adaptive stripe width;
- RTT-spread guard;
- benchmarks at 1 / 4 / 16 / 64 / 128 / 196 / 256 channels.

The scheduler already contains `PickStripe()` with an RTT-spread limit, but
it is not wired into packet transmission yet.


## Managing many channels

For large pools, keep the channel inventory in CSV instead of hand-writing
hundreds of INI sections.

Start from:

```text
examples/channels.example.csv
```

Generate a client config:

```bash
python3 scripts/generate-multibond-config.py \
  --role client \
  --channels channels.csv \
  --out client.conf \
  --bond-max 256 \
  --bond-active 196
```

Generate an exit config:

```bash
python3 scripts/generate-multibond-config.py \
  --role exit \
  --channels channels.csv \
  --out exit.conf \
  --bond-max 256 \
  --bond-active 196 \
  --mode l3
```

## Yandex Table / Disk inventory

The channel inventory can be maintained as a CSV/XLSX table and synchronized
before generating OpenFlux configs.

Supported sources:

- local CSV or XLSX;
- a direct HTTP(S) CSV/XLSX URL;
- a public Yandex Disk link with downloading enabled.

The first XLSX worksheet is used. Required columns are `name` and `type`.
The normalized inventory columns are:

```text
name,type,priority,url,dial,listen,token,uid,enabled
```

Rows with `enabled=0`, `false`, `no`, `off` or `disabled` are skipped by
the config generator. This lets channels be disabled in the table without
deleting them.

Example:

```bash
python3 scripts/sync-yandex-channels.py \
  --source 'YOUR_PUBLIC_YANDEX_DISK_OR_TABLE_LINK' \
  --out channels.csv

python3 scripts/generate-multibond-config.py \
  --role client --channels channels.csv --out client.conf \
  --bond-max 256 --bond-active 196

python3 scripts/generate-multibond-config.py \
  --role exit --channels channels.csv --out exit.conf \
  --bond-max 256 --bond-active 196 --mode l3
```

The sync writes `channels.csv` atomically, so a failed download or parse does
not replace the last good inventory.

## Linux exit service

After a successful smoke test:

```bash
sudo scripts/install-exit-systemd.sh exit.conf secret.txt --start
```

Useful commands:

```bash
sudo systemctl status openflux-multibond
sudo journalctl -fu openflux-multibond
sudo scripts/multibond-doctor.sh
```

The installed service uses:

```text
/usr/local/bin/openflux-multibond
/etc/openflux-multibond/exit.conf
/var/lib/openflux-multibond/secret.txt
/var/lib/openflux-multibond/        mutable cookie/state files
```

## Validation

From a source checkout:

```bash
bash scripts/multibond-smoke.sh
```

This runs scheduler/session tests, the full Go test suite, `go vet`, a native
build and Linux/macOS/Windows cross-builds.


## Live Yandex inventory hot reload

A client can now treat the normalized channel inventory as the runtime source of
truth without restarting OpenFlux. The client owns reconciliation and sends
encrypted `TransportStart` / `TransportStop` control messages to the exit,
so the exit does not need to poll Yandex separately.

Data path:

```text
Yandex Disk / public table / CSV / XLSX
        |
        | sync-yandex-channels.py --interval 15
        v
normalized channels.csv
        |
        | --channel-inventory + 5s poll
        v
client inventory reconciler
        |
        +-- unchanged -> no action
        +-- added     -> local runtime start + peer TransportStart
        +-- removed   -> peer TransportStop + local runtime stop
        +-- changed   -> safe stop/recreate on both peers
        |
        v
MultiBond ACTIVE / RESERVE / FAILED
```

Start the Yandex/source watcher:

```bash
python3 scripts/sync-yandex-channels.py \
  --source 'YOUR_PUBLIC_YANDEX_DISK_OR_TABLE_LINK' \
  --out channels.csv \
  --interval 15
```

Start the client with a bootstrap carrier plus hot reload:

```bash
./openflux \
  --config client.conf \
  --channel-inventory channels.csv \
  --channel-inventory-interval 5s
```

The bootstrap carrier is important: it gives the client a stable encrypted
control path while inventory-managed carriers are added or replaced. A
bootstrap transport that is not listed in the inventory is never removed by
the reconciler.

Safety properties:

- a download or parse failure never replaces the last known-good CSV;
- identical source content is not rewritten;
- disabled rows are removed from the managed pool;
- a changed row (URL, priority, direct endpoint, token/UID or type) is replaced
  at runtime;
- the reconciler refuses to remove or replace the last currently-live carrier;
- direct rows must provide both `dial` (client side) and `listen` (exit side);
- the exit receives lifecycle changes through the authenticated encrypted
  Session control channel.

The inventory remains:

```text
name,type,priority,url,dial,listen,token,uid,enabled
```

Example:

```csv
name,type,priority,url,dial,listen,token,uid,enabled
volga-01,vyandex,70,https://example.invalid/doc-1,,,,,1
boards-01,boards,60,https://example.invalid/board-1,,,,,1
direct-backup,direct,100,,203.0.113.10:18445,0.0.0.0:18445,,,1
```

For a direct row, the same inventory line creates a client-side dialer from
`dial` and an exit-side listener from `listen`. Other document transports
use the same URL on both peers.
