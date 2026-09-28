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
- reconnect penalty and loss input in the score;
- weighted rendezvous hashing so ordinary IP flows stay stable on one carrier;
- bounded background RTT probing: 8 carriers/second;
- runtime channel add/remove support;
- transport agnostic: Yandex legacy, Volga, Boards, Mail.ru, Cups, MAX/WebRTC and Direct can share the pool;
- scheduler snapshot API for future status UI/API.

Default score:

- throughput: 50%;
- averaged RTT: 30%;
- packet-loss input: 15%;
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
RTT probing is deliberately bounded rather than pinging all 256 channels every
second.

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
