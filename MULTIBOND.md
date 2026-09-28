# OpenFlux MultiBond — v0.1 core

Base upstream commit: `3fda9695ac3b8a69e2c9f68576664248f9fbbdab` (`p1neappleXpress/OpenFlux`, main).

## Goal

Adaptive multi-transport scheduler for an OpenFlux fork.

Initial policy:

- up to **256** physical channels;
- target up to **196 active** channels;
- remaining connected channels are reserve;
- preferred averaged RTT: **<= 120 ms**;
- normal hard RTT ceiling: **150 ms**;
- emergency ceiling: **300 ms** (used only to keep `MinActive` connectivity);
- RTT is an EWMA (`alpha=0.20`) so one spike does not remove a channel;
- throughput has more weight than latency, so a slightly higher-ping but much faster link can still win;
- striped groups are limited to **30 ms RTT spread** to reduce reordering;
- ordinary flows use weighted rendezvous hashing to remain stable on one carrier.

## Default score

Approximate weighting:

- throughput: 50%
- averaged RTT: 30%
- loss: 15%
- stability/reconnects: 5%

A channel must remain above the RTT limit for `BadRTTHold` before normal demotion.
Recovery is also delayed by `RecoveryHold` to avoid flapping.

## Current code

`transport/bond/` contains a transport-agnostic scheduler. It has no dependency on Yandex, Volga, Mail.ru, MAX, Cups, or Direct, so all of them can enter the same pool.

Implemented:

- channel registration / hard 256 limit;
- active / reserve / failed states;
- EWMA RTT;
- ping/pong RTT measurement hooks;
- EWMA throughput samples;
- loss and reconnect penalties;
- normal and emergency pools;
- weighted flow selection;
- RTT-bounded stripe-group selection;
- tests for spike filtering, sustained high latency, throughput-vs-latency, pool limit, emergency fallback and stripe RTT spread.

## Next integration step

Hook the scheduler into upstream `transport.Session`:

1. Enable with CLI flags only when requested (`--bond`).
2. Feed `LinkPing` / `LinkPong` into `NotePingSent` / `NotePong`.
3. Feed successful payload bytes into `ObserveBytes`.
4. Rebalance once per second.
5. Replace the existing equal-priority flow hash selector in `Session.Send()` with `PickFlow()` while preserving the old selector as fallback.
6. Keep phase 1 flow-stable (no per-packet striping yet).
7. Add protocol-compatible reorder/striping as phase 2 after real 1/4/16/64/128/196/256-channel benchmarks.

## Important

Phase 1 intentionally does **not** stripe packets of one TCP flow across many channels. That would create large packet reordering and can reduce TCP throughput. The scheduler already exposes `PickStripe()` for phase 2, but real striping needs a receive-side reorder layer/protocol work first.
