# hookfuzz

Chaos testing for payment webhook handlers.

hookfuzz finds bugs in payment integrations by deliberately delivering webhooks
twice, out of order, late, or after failures, then checking that your app still
ends up in a correct state. When it finds a bug, it shrinks it to the smallest
sequence of events that reproduces it.

![demo](demo.gif)

## Why

Stripe delivers webhooks **at least once** and **in no particular order**. The
same event can arrive twice, a refund can arrive before its charge, and a stale
`subscription.updated` can land after a newer one. Most integrations are only
tested on the happy path, so these bugs are usually found by customers.
`stripe trigger` sends one event, once, in order. hookfuzz is adversarial and
then checks correctness.

## Quickstart (sample shop)

Requires Go 1.22+ and Node 22+.

```bash
make build
cd sample-app && npm install && npm start &   # buggy mode on :4242
./hookfuzz run
```

```
FAIL invariant "one_shipment_per_session" (seed 1)
  session_id=cs_0014 appears 2 times in shipments (max 1)
Minimal reproduction (2 of 44 deliveries):
  1. checkout.session.completed     cs_0014    evt_0016
  2. checkout.session.completed     cs_0014    evt_0016  <- duplicate
Replay: hookfuzz replay --seed 1 --config hookfuzz.yaml
```

Try the fixed version: `HOOKFUZZ_APP_MODE=fixed npm start`, then `./hookfuzz run`
reports `PASS 100 runs (seeds 1..100), 4 invariants held`.

The sample shop has three planted bugs (see [sample-app/handlers.js](sample-app/handlers.js)):
a non-idempotent fulfillment handler, a refund that is dropped when it arrives
before its charge, and a subscription status that a stale update can overwrite.
The e2e tests assert hookfuzz finds and shrinks each one.

## How it works

1. **Scenarios** generate a realistic event history (`checkout`, `refund`,
   `subscription`). Payloads are Stripe-shaped and signed with a valid
   `Stripe-Signature`, so your real handler (including `constructEvent`) runs
   unmodified.
2. **Faults** turn that history into a seeded delivery schedule with duplicates,
   reordering within a window, delays, and redeliveries after simulated timeouts.
   Any non-2xx response is retried the way Stripe does it (3 attempts).
3. **Invariants** from `hookfuzz.yaml` are checked against your app's state after
   each run. Ordering rules compare against event `created` timestamps, not arrival order.
4. **Shrinking** (delta debugging) removes deliveries until the failure is
   minimal, then prints the timeline and the seed. `hookfuzz replay --seed N`
   reproduces it exactly.

## Using it on your app

Add two test-only endpoints to your app (never expose them in production):

- `GET /__hookfuzz/state` returns a JSON object mapping a collection name to an
  array of objects, e.g. `{"shipments": [{"session_id": "cs_1"}]}`
- `POST /__hookfuzz/reset` clears all state (hookfuzz calls it before every run
  and every shrink attempt, so make it fast)

Point `hookfuzz.yaml` at your app, set `signing_secret` to the webhook secret
your app verifies with, and declare invariants.

### Invariant kinds

| kind | checks | fields |
|---|---|---|
| `max_count_per_key` | no `key` value appears more than `max` times in `collection` | `key`, `max` |
| `field_le` | for every object, `left <= right` | `left`, `right` |
| `matches_latest_event` | each object's `field` equals `event_field` on the newest (by `created`) acknowledged event for that object | `field`, `event_field`, `event_types` |

A missing collection or field counts as a violation, so a typo can't hide a bug.

### Commands

```
hookfuzz run    [--config hookfuzz.yaml] [--seed 1] [--runs 100]
hookfuzz replay --seed N [--config hookfuzz.yaml]
```

Exit codes: `0` all invariants held, `1` an invariant failed, `2` usage or setup error
(including an app that rejects every delivery), `130` interrupted. If shrinking is
interrupted after a bug is found, the FAIL report is still printed.

## Limitations

- Deliveries are sent one at a time so runs are reproducible. Concurrency races
  inside a single handler are out of scope.
- One webhook endpoint per config.
- Only Stripe-shaped events.

## Development

```bash
make test   # Go unit tests + sample app tests
make e2e    # hookfuzz against the real sample app: finds all 3 bugs, no false positives on the fixed app
```
