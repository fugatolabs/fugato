const { test } = require('node:test');
const assert = require('node:assert/strict');
const { page } = require('./server.cjs');

test('website and admin pages identify the selected environment', () => {
  assert.match(page('Website', 'fixture-one'), /<h1>Website<\/h1>/);
  assert.match(page('Admin', 'fixture-two'), /Runtime: fixture-two/);
});
