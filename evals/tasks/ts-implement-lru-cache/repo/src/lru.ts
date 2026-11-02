/**
 * A fixed-capacity cache that evicts the least recently used entry.
 *
 * - `new LRUCache(capacity)` throws a `RangeError` unless `capacity` is an
 *   integer >= 1.
 * - `get(key)` returns the stored value, or `undefined` when the key is
 *   missing. A successful `get` marks the key as most recently used.
 * - `set(key, value)` inserts or updates the entry and marks it as most
 *   recently used. When an insert makes the cache exceed its capacity, the
 *   least recently used entry is evicted and passed to the `onEvict` callback
 *   (if one was given) as `onEvict(key, value)`. Updating an existing key never
 *   evicts anything. `set` returns the cache itself so calls can be chained.
 * - `has(key)` reports whether the key is present WITHOUT changing recency.
 * - `delete(key)` removes the entry and returns whether it existed.
 * - `size` is the current number of entries.
 * - `keys()` returns the keys ordered from least to most recently used.
 */
export class LRUCache<K, V> {
  constructor(capacity: number, onEvict?: (key: K, value: V) => void) {
    throw new Error('not implemented');
  }

  get size(): number {
    throw new Error('not implemented');
  }

  get(key: K): V | undefined {
    throw new Error('not implemented');
  }

  set(key: K, value: V): this {
    throw new Error('not implemented');
  }

  has(key: K): boolean {
    throw new Error('not implemented');
  }

  delete(key: K): boolean {
    throw new Error('not implemented');
  }

  keys(): K[] {
    throw new Error('not implemented');
  }
}
