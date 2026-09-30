// Two versions of the same webhook handlers. `buggy` has the three planted
// bugs hookfuzz should find; `fixed` shows the standard fix for each.

function dispatch(table, store, event) {
  const handle = table[event.type];
  if (handle) handle(store, event);
}

function ship(store, session) {
  store.shipments.push({
    order_id: `ord_${store.shipments.length + 1}`,
    session_id: session.id,
    amount: session.amount_total,
  });
}

const buggyTable = {
  // BUG 1 (not idempotent): every delivery ships an order, so a redelivered
  // event ships the same checkout twice.
  'checkout.session.completed'(store, event) {
    ship(store, event.data.object);
  },

  // BUG 2 (order-dependent): charge.succeeded overwrites the stored charge,
  // and charge.refunded is dropped if the charge isn't stored yet. Either way
  // a refund that arrives "early" is lost.
  'charge.succeeded'(store, event) {
    const charge = event.data.object;
    store.charges.set(charge.id, { id: charge.id, captured: charge.amount_captured, refunded: charge.amount_refunded });
  },
  'charge.refunded'(store, event) {
    const charge = event.data.object;
    const existing = store.charges.get(charge.id);
    if (!existing) return;
    existing.refunded = charge.amount_refunded;
  },

  // BUG 3 (stale overwrite): the last delivery wins, even when it is older.
  'customer.subscription.created': setSubscriptionStatus,
  'customer.subscription.updated': setSubscriptionStatus,
  'customer.subscription.deleted': setSubscriptionStatus,
};

function setSubscriptionStatus(store, event) {
  const sub = event.data.object;
  store.subscriptions.set(sub.id, { id: sub.id, status: sub.status });
}

const fixedTable = {
  'checkout.session.completed'(store, event) {
    ship(store, event.data.object);
  },
  'charge.succeeded': mergeCharge,
  'charge.refunded': mergeCharge,
  'customer.subscription.created': applySubscriptionIfNewer,
  'customer.subscription.updated': applySubscriptionIfNewer,
  'customer.subscription.deleted': applySubscriptionIfNewer,
};

// FIX 2: merge instead of overwrite, and accept a refund for a charge we
// haven't seen yet. amount_refunded only grows, so the larger value is newer.
function mergeCharge(store, event) {
  const charge = event.data.object;
  const existing = store.charges.get(charge.id);
  store.charges.set(charge.id, {
    id: charge.id,
    captured: charge.amount_captured,
    refunded: Math.max(existing ? existing.refunded : 0, charge.amount_refunded),
  });
}

// FIX 3: compare event.created, not arrival order, and ignore older events.
function applySubscriptionIfNewer(store, event) {
  const sub = event.data.object;
  const existing = store.subscriptions.get(sub.id);
  if (existing && existing.last_event_created >= event.created) return;
  store.subscriptions.set(sub.id, { id: sub.id, status: sub.status, last_event_created: event.created });
}

module.exports = {
  buggy: {
    handle(store, event) {
      dispatch(buggyTable, store, event);
    },
  },
  fixed: {
    handle(store, event) {
      // FIX 1: remember processed event IDs and skip redeliveries.
      if (store.processedEvents.has(event.id)) return;
      store.processedEvents.add(event.id);
      dispatch(fixedTable, store, event);
    },
  },
};
