# MultiBond quick start

This is the recommended first bring-up sequence for `multibond-v0.1`.
Start with two direct TCP carriers. This validates Session encryption,
RTT measurement, adaptive scheduling and failover without involving
third-party document transports.

## 1. Build

Run on both machines:

```bash
git clone https://github.com/ByteBananaDev/OpenFlux-MultiBond.git
cd OpenFlux-MultiBond
git checkout multibond-v0.1
go mod download
go build -o openflux .
```

Optional smoke suite:

```bash
bash scripts/multibond-smoke.sh
```

## 2. Create one shared secret

Create on the exit node:

```bash
openssl rand -base64 48 > secret.txt
chmod 600 secret.txt
```

Copy the same `secret.txt` to the client over a secure channel.
Do not use different keys.

## 3. Exit config

Create `exit-smoke.conf`:

```ini
[Interface]
Role = exit
Mode = l4
EncryptionKeyFile = secret.txt
SessionContext = multibond-smoke-v01

Bond = true
BondMax = 8
BondActive = 2
BondMinActive = 1
BondPreferredRTT = 120ms
BondMaxRTT = 150ms
BondEmergencyRTT = 300ms
BondRTTSpread = 30ms

[Transport "direct-01"]
Type = direct
Priority = 50
Listen = 0.0.0.0:8445

[Transport "direct-02"]
Type = direct
Priority = 50
Listen = 0.0.0.0:8446
```

Allow TCP ports 8445 and 8446 in the VPS firewall.

Start:

```bash
./openflux --config=exit-smoke.conf --debug=2
```

Expected log after the client connects:

```text
MultiBond: enabled ...
[BOND] pool total=2 active=2 reserve=0 failed=0
```

## 4. Client config

Create `client-smoke.conf` and replace `EXIT_IP`:

```ini
[Interface]
Role = client
Inbound = socks5
Socks5 = :1080
EncryptionKeyFile = secret.txt
SessionContext = multibond-smoke-v01

Bond = true
BondMax = 8
BondActive = 2
BondMinActive = 1
BondPreferredRTT = 120ms
BondMaxRTT = 150ms
BondEmergencyRTT = 300ms
BondRTTSpread = 30ms

[Transport "direct-01"]
Type = direct
Priority = 50
Dial = EXIT_IP:8445

[Transport "direct-02"]
Type = direct
Priority = 50
Dial = EXIT_IP:8446
```

Start:

```bash
./openflux --config=client-smoke.conf --debug=2
```

## 5. Verify traffic

On the client:

```bash
curl --socks5-hostname 127.0.0.1:1080 https://api.ipify.org ; echo
```

The returned address should be the exit node's public address.

Watch the OpenFlux logs. MultiBond should keep both direct carriers in the
active pool and periodically update their RTT.

For a continuous traffic check, open another terminal on the client:

```bash
bash scripts/multibond-live-watch.sh 127.0.0.1:1080 https://api.ipify.org 120 1
```

The watcher sends 120 requests one second apart and prints a final
`SUMMARY ok=... fail=...`. Keep it running during the failover test below.

## 6. Failover test

Temporarily block one carrier on the exit:

```bash
sudo iptables -I INPUT 1 -p tcp --dport 8445 -j DROP
```

Continue sending requests through SOCKS5. After liveness detection, the pool
should fall back to `direct-02`. The live watcher should continue reporting
successful requests; any failed requests are counted in its final summary.

Remove the temporary rule:

```bash
sudo iptables -D INPUT -p tcp --dport 8445 -j DROP
```

The recovered channel should eventually return after the recovery hold.

## 7. Add real transports

After the two-direct smoke succeeds, replace or supplement the direct channels
with uniquely named transport sections, for example:

```ini
[Transport "volga-01"]
Type = vyandex
Priority = 50
URL = YOUR_DOCUMENT_01

[Transport "volga-02"]
Type = vyandex
Priority = 50
URL = YOUR_DOCUMENT_02

[Transport "mail-01"]
Type = mailru
Priority = 50
URL = YOUR_MAILRU_LINK
```

Use corresponding carrier definitions on both peers and keep the same
`SessionContext` and encryption secret.

Scale gradually:

```text
2 -> 4 -> 8 -> 16 -> 32 -> 64 -> 128 -> 196 active
```

Do not jump to 196 immediately. Record throughput, RTT distribution,
reconnects and failures at each step.

## 8. Full tunnel after smoke

For a Linux exit, change:

```ini
Mode = l3
```

and run as root / with the permissions required by the upstream L3 backend.

On macOS, change the client to:

```ini
Inbound = tun
```

and start it with `sudo`.

Phase 1 balances whole IPv4 flows. It does not yet stripe one TCP flow across
multiple carriers. Per-flow striping and reorder buffering are phase 2.
