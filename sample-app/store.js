// In-memory state for the sample shop. Real apps would use a database.
function createStore() {
  return {
    shipments: [], // { order_id, session_id, amount }
    charges: new Map(), // id -> { id, captured, refunded }
    subscriptions: new Map(), // id -> { id, status, last_event_created? }
    processedEvents: new Set(), // event IDs (fixed mode only)
  };
}

// The JSON shape hookfuzz reads from GET /__hookfuzz/state.
function snapshot(store) {
  return {
    shipments: store.shipments,
    charges: [...store.charges.values()].map(({ id, captured, refunded }) => ({ id, captured, refunded })),
    subscriptions: [...store.subscriptions.values()].map(({ id, status }) => ({ id, status })),
  };
}

module.exports = { createStore, snapshot };
