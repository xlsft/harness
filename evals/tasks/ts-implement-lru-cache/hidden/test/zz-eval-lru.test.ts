import { test } from 'node:test';
import assert from 'node:assert/strict';
import { LRUCache } from '../src/lru.ts';

test('rejects invalid capacity', () => {
  assert.throws(() => new LRUCache(0), RangeError);
  assert.throws(() => new LRUCache(-1), RangeError);
  assert.throws(() => new LRUCache(1.5), RangeError);
  assert.doesNotThrow(() => new LRUCache(1));
});

test('has() does not touch recency; get() does', () => {
  const c = new LRUCache<string, number>(2);
  c.set('a', 1).set('b', 2);
  assert.equal(c.has('a'), true);
  c.set('c', 3); // evicts a: has() must not have refreshed it
  assert.deepEqual(c.keys(), ['b', 'c']);
  c.get('b');
  c.set('d', 4);
  assert.deepEqual(c.keys(), ['b', 'd']);
  assert.equal(c.get('zzz'), undefined);
});

test('update refreshes recency and never evicts', () => {
  const evicted: Array<[string, number]> = [];
  const c = new LRUCache<string, number>(2, (k, v) => evicted.push([k, v]));
  c.set('a', 1).set('b', 2).set('a', 10);
  assert.deepEqual(evicted, []);
  assert.equal(c.size, 2);
  c.set('c', 3);
  assert.deepEqual(evicted, [['b', 2]]);
  assert.equal(c.get('a'), 10);
  assert.deepEqual(c.keys(), ['c', 'a']);
});

test('delete and falsy values', () => {
  const c = new LRUCache<string, number | null>(3);
  c.set('zero', 0).set('null', null);
  assert.equal(c.get('zero'), 0);
  assert.equal(c.has('null'), true);
  assert.equal(c.delete('zero'), true);
  assert.equal(c.delete('zero'), false);
  assert.equal(c.size, 1);
});

test('capacity of one', () => {
  const c = new LRUCache<number, string>(1);
  c.set(1, 'x').set(2, 'y');
  assert.deepEqual(c.keys(), [2]);
});
