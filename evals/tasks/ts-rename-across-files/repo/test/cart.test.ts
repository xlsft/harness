import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Cart } from '../src/cart.ts';
import { buildInvoice } from '../src/invoice.ts';

test('cart merges items and totals with tax', () => {
  const c = new Cart(0.1);
  c.add({ sku: 'A', unitPrice: 250, quantity: 2 });
  c.add({ sku: 'A', unitPrice: 250, quantity: 1 });
  c.add({ sku: 'B', unitPrice: 1000, quantity: 1 });
  assert.equal(c.total(), 1925);
  assert.equal(c.label(), '$19.25');
});

test('invoice total matches cart', () => {
  const inv = buildInvoice([{ sku: 'B', unitPrice: 999, quantity: 3 }], 0.2);
  assert.equal(inv.total, '$35.96');
  assert.deepEqual(inv.lines, ['B x3 $29.97']);
});
