export class LRUCache<K, V> {
  #map = new Map<K, V>();
  #capacity: number;
  #onEvict?: (key: K, value: V) => void;

  constructor(capacity: number, onEvict?: (key: K, value: V) => void) {
    if (!Number.isInteger(capacity) || capacity < 1) throw new RangeError('capacity must be an integer >= 1');
    this.#capacity = capacity;
    this.#onEvict = onEvict;
  }

  get size(): number {
    return this.#map.size;
  }

  get(key: K): V | undefined {
    if (!this.#map.has(key)) return undefined;
    const v = this.#map.get(key) as V;
    this.#map.delete(key);
    this.#map.set(key, v);
    return v;
  }

  set(key: K, value: V): this {
    const existed = this.#map.delete(key);
    this.#map.set(key, value);
    if (!existed && this.#map.size > this.#capacity) {
      const [k, v] = this.#map.entries().next().value as [K, V];
      this.#map.delete(k);
      this.#onEvict?.(k, v);
    }
    return this;
  }

  has(key: K): boolean {
    return this.#map.has(key);
  }

  delete(key: K): boolean {
    return this.#map.delete(key);
  }

  keys(): K[] {
    return [...this.#map.keys()];
  }
}
