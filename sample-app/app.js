const express = require('express');
const Stripe = require('stripe');
const { createStore, snapshot } = require('./store');
const handlers = require('./handlers');

function createApp({ mode = 'buggy', secret }) {
  const handler = handlers[mode];
  if (!handler) throw new Error(`unknown mode "${mode}"; use "buggy" or "fixed"`);
  const stripe = new Stripe('sk_test_unused'); // only used for webhook verification
  let store = createStore();
  const app = express();

  // Signature verification needs the raw body, so don't parse JSON first.
  app.post('/webhook', express.raw({ type: 'application/json' }), (req, res) => {
    let event;
    try {
      event = stripe.webhooks.constructEvent(req.body, req.get('stripe-signature'), secret);
    } catch (err) {
      return res.status(400).send(`signature verification failed: ${err.message}`);
    }
    handler.handle(store, event);
    res.json({ received: true });
  });

  // Test-only inspection endpoints used by hookfuzz. Never expose these in production.
  app.get('/__hookfuzz/state', (req, res) => res.json(snapshot(store)));
  app.post('/__hookfuzz/reset', (req, res) => {
    store = createStore();
    res.json({ reset: true });
  });

  return app;
}

module.exports = { createApp };
