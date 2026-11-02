import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as pricing from '../src/pricing.ts';

test('computeOrderTotal is exported and calcTotal is gone', () => {
  assert.equal(typeof (pricing as Record<string, unknown>).computeOrderTotal, 'function');
  assert.equal((pricing as Record<string, unknown>).calcTotal, undefined);
  const fn = (pricing as any).computeOrderTotal;
  assert.equal(fn([{ sku: 'X', unitPrice: 333, quantity: 3 }], 0.075), 1074);
  assert.equal(fn([], 0.2), 0);
});
