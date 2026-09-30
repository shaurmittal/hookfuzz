const { createApp } = require('./app');

const port = Number(process.env.PORT || 4242);
const mode = process.env.HOOKFUZZ_APP_MODE || 'buggy';
const secret = process.env.STRIPE_WEBHOOK_SECRET || 'whsec_test_hookfuzz';

createApp({ mode, secret }).listen(port, () => {
  console.log(`sample shop (${mode}) listening on http://localhost:${port}`);
});
