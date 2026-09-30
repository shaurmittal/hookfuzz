# hookfuzz: chaos testing for payment webhook handlers

> **Status:** in active development. Design is done; the MVP (Go CLI + sample app) is being built now.

## One-liner
hookfuzz finds bugs in payment integrations by deliberately delivering webhooks twice, out of order, late, or after failures, then checking that the app still ends up in a correct state. When it finds a bug, it shrinks it to the smallest sequence of events that reproduces it.

## The problem
Payment providers like Stripe send webhooks to tell your server what happened (`payment_intent.succeeded`, `charge.refunded`, `customer.subscription.updated`, ...). Delivery is **at-least-once** and **not ordered**:

- The same event can arrive **more than once**.
- Events can arrive **out of order**.
- Events are **retried** when your server times out or returns an error.

Most integrations are only tested on the happy path, so bugs hide in these edges:

| Bug | Real-world cost |
|---|---|
| Fulfillment handler isn't idempotent | Customer gets shipped two orders for one payment |
| Refund arrives before the charge is recorded | Refund is dropped or the order's state is corrupted |
| Stale `subscription.updated` arrives after a newer one | A cancelled customer is marked active (or the reverse) |

These bugs are rare, hard to reproduce, and usually found by customers first.

## How it works
1. **Scenario generation.** Builds a realistic event history for a flow (checkout, refund, subscription lifecycle). Payloads are Stripe-shaped and signed with a valid `Stripe-Signature` HMAC, so the app's real handler code runs unmodified.
2. **Fault-injecting delivery.** A scheduler applies a *seeded* fault plan and POSTs events to the app:
   - duplicate delivery
   - reordering within a window
   - delays
   - retry after a failed (non-2xx) response
3. **Invariant checking.** After each run, it checks user-declared rules against the app's state through a small inspection endpoint, e.g.:
   - `orders shipped per checkout session <= 1`
   - `amount refunded <= amount captured`
   - `final subscription status == status in the latest event (by created timestamp)`
4. **Shrinking.** When a rule fails, it runs delta debugging over the delivery sequence and removes events until the failure is as small as possible. It prints the result as a readable timeline plus the seed, so the failure replays exactly the same way every time.

Example output:
```
FAIL invariant "one_shipment_per_session" (seed 48213)
Minimal reproduction (3 of 41 events):
  1. checkout.session.completed   cs_123
  2. checkout.session.completed   cs_123   <- duplicate
  3. (handler shipped order twice)
Replay: hookfuzz replay --seed 48213
```

## Tech stack & why
- **Go** for the CLI and delivery engine: a single static binary, easy concurrency for the scheduler, and one of Stripe's main languages.
- **Node/Express sample shop** with three planted bugs (non-idempotent fulfillment, order-dependent refund, stale-update overwrite) to show what the tool catches.
- **Seeded randomness** everywhere, so every run is reproducible and suitable for CI.

## Scope (MVP)
**In:** CLI, 3 scenarios, 4 fault types, invariants in a config file, shrinking, sample app, README demo GIF.
**Out (future):** recording and replaying real Stripe traffic, web UI, plugins for other providers (Adyen, PayPal), a GitHub Action.

---

## Questions recruiters and engineers might ask

**Why did you build this?**
Payment bugs almost never happen on the happy path. They show up in retries, duplicates, and ordering. Stripe's own docs tell developers to handle all of these, but there's no easy way to *test* that you did. I wanted a tool that proves it instead of hoping.

**Isn't this just Stripe CLI's `stripe trigger`?**
No. `stripe trigger` sends one event, once, in order, which is the happy path. hookfuzz is adversarial: it mixes up delivery on purpose and then *checks correctness*, which `trigger` doesn't do.

**What's the hardest technical part?**
Two things:
1. **Choosing invariants that survive reordering.** You have to compare against event `created` timestamps, not arrival order. Otherwise the checker has the same bug as the app.
2. **Keeping shrinking fast.** Each shrink attempt re-runs a stateful app, so state has to be reset cheaply between runs, and events are removed in chunks first, then one at a time.

**How do you know the tool itself is correct?**
The sample app has known, planted bugs, and tests assert that hookfuzz finds each one and shrinks it to the expected minimal sequence. There's also a "fixed" version of the app, and tests assert that hookfuzz reports **no** false positives against it.

**What's idempotency, in one sentence?**
Doing the same thing twice has the same effect as doing it once. That's what the fix usually looks like: store processed event IDs and skip repeats.

**How does this connect to Stripe's work?**
It's the same class of problem Stripe cares about internally: reliability at scale, safe money movement (the posting mentions "safer payouts"), and helping developers integrate correctly. The posting also asks for high-quality PRs with good test coverage, and this is a testing tool at its core.

**Did you use AI?**
Yes, to move faster on boilerplate like payload shapes and the CLI setup. The invariant checker and shrinker are the parts I design and verify by hand, because a testing tool that's wrong gives false confidence, which is worse than no tool.

**What would you do next?**
Record real test-mode traffic and replay it with faults, ship a GitHub Action so it runs in CI on every PR, and support multiple webhook endpoints (e.g. separate billing and fulfillment services).

**What did you learn?**
*(Fill in after building.)* For example, which planted bug was hardest to catch, or a surprise about how often reordering matters.

## Talking points to remember
- **At-least-once, unordered delivery** is the core problem.
- **Idempotency + ordering by `created` timestamp** is the core fix.
- **Seeded + shrinking** = reproducible, minimal bug reports.
- Be honest: *"The design is done and I'm building the MVP now."*
