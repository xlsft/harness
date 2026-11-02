import { test } from 'node:test';
import assert from 'node:assert/strict';
import { LRUCache } from '../src/lru.ts';

test('stores and returns values', () => {
  const c = new LRUCache<string, number>(2);
  c.set('a', 1).set('b', 2);
  assert.equal(c.get('a'), 1);
  assert.equal(c.size, 2);
});

test('evicts the least recently used entry', () => {
  const c = new LRUCache<string, number>(2);
  c.set('a', 1).set('b', 2);
  c.get('a');
  c.set('c', 3);
  assert.equal(c.has('b'), false);
  assert.deepEqual(c.keys(), ['a', 'c']);
});
