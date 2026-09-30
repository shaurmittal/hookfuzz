const test = require('node:test');
const assert = require('node:assert/strict');
const Stripe = require('stripe');
const { createApp } = require('../app');

const secret = 'whsec_unit_test';
const stripe = new Stripe('sk_test_unused');

async function startShop(t, mode) {
  const server = createApp({ mode, secret }).listen(0);
  await new Promise((resolve) => server.once('listening', resolve));
  t.after(() => {
    server.closeAllConnections();
    server.close();
  });
  const base = `http://127.0.0.1:${server.address().port}`;
  return {
    async send(event, { signature } = {}) {
      const payload = JSON.stringify(event);
      const header = signature ?? stripe.webhooks.generateTestHeaderString({ payload, secret });
      const res = await fetch(`${base}/webhook`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', 'stripe-signature': header },
        body: payload,
      });
      return res.status;
    },
    async state() {
      return (await fetch(`${base}/__hookfuzz/state`)).json();
    },
    async reset() {
      return (await fetch(`${base}/__hookfuzz/reset`, { method: 'POST' })).status;
    },
  };
}

let seq = 0;
function event(type, created, object) {
  seq += 1;
  return { id: `evt_${seq}`, object: 'event', type, created, api_version: '2024-06-20', livemode: false, data: { object } };
}
const session = (id) => ({ id, object: 'checkout.session', amount_total: 1000 });
const charge = (id, amountRefunded) => ({ id, object: 'charge', amount: 1000, amount_captured: 1000, amount_refunded: amountRefunded });
const sub = (id, status) => ({ id, object: 'subscription', status });

test('rejects a bad signature', async (t) => {
  const shop = await startShop(t, 'buggy');
  const status = await shop.send(event('checkout.session.completed', 1, session('cs_1')), { signature: 't=1,v1=deadbeef' });
  assert.equal(status, 400);
  assert.deepEqual((await shop.state()).shipments, []);
});

test('buggy: a redelivered checkout ships twice', async (t) => {
  const shop = await startShop(t, 'buggy');
  const ev = event('checkout.session.completed', 1, session('cs_1'));
  assert.equal(await shop.send(ev), 200);
  assert.equal(await shop.send(ev), 200);
  assert.equal((await shop.state()).shipments.length, 2);
});

test('fixed: a redelivered checkout ships once', async (t) => {
  const shop = await startShop(t, 'fixed');
  const ev = event('checkout.session.completed', 1, session('cs_1'));
  await shop.send(ev);
  await shop.send(ev);
  assert.equal((await shop.state()).shipments.length, 1);
});

test('buggy: a refund that arrives before its charge is dropped', async (t) => {
  const shop = await startShop(t, 'buggy');
  await shop.send(event('charge.refunded', 2, charge('ch_1', 500)));
  await shop.send(event('charge.succeeded', 1, charge('ch_1', 0)));
  assert.deepEqual((await shop.state()).charges, [{ id: 'ch_1', captured: 1000, refunded: 0 }]);
});

test('fixed: a refund that arrives before its charge is kept', async (t) => {
  const shop = await startShop(t, 'fixed');
  await shop.send(event('charge.refunded', 2, charge('ch_1', 500)));
  await shop.send(event('charge.succeeded', 1, charge('ch_1', 0)));
  assert.deepEqual((await shop.state()).charges, [{ id: 'ch_1', captured: 1000, refunded: 500 }]);
});

test('buggy: a stale subscription update overwrites the newer one', async (t) => {
  const shop = await startShop(t, 'buggy');
  await shop.send(event('customer.subscription.updated', 3, sub('sub_1', 'past_due')));
  await shop.send(event('customer.subscription.created', 1, sub('sub_1', 'active')));
  assert.deepEqual((await shop.state()).subscriptions, [{ id: 'sub_1', status: 'active' }]);
});

test('fixed: a stale subscription update is ignored', async (t) => {
  const shop = await startShop(t, 'fixed');
  await shop.send(event('customer.subscription.updated', 3, sub('sub_1', 'past_due')));
  await shop.send(event('customer.subscription.created', 1, sub('sub_1', 'active')));
  assert.deepEqual((await shop.state()).subscriptions, [{ id: 'sub_1', status: 'past_due' }]);
});

test('reset clears all state, including processed event IDs', async (t) => {
  const shop = await startShop(t, 'fixed');
  const ev = event('checkout.session.completed', 1, session('cs_1'));
  await shop.send(ev);
  assert.equal(await shop.reset(), 200);
  assert.deepEqual(await shop.state(), { shipments: [], charges: [], subscriptions: [] });
  await shop.send(ev);
  assert.equal((await shop.state()).shipments.length, 1);
});

test('rejects an unknown mode', () => {
  assert.throws(() => createApp({ mode: 'chaotic', secret }), /unknown mode/);
});
