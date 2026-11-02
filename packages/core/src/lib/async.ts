/** Small async primitives: a FIFO mutex and an unbounded async queue. */

/** Serializes async critical sections (FIFO). */
export class Mutex {
  private tail: Promise<void> = Promise.resolve();

  run<T>(fn: () => Promise<T>): Promise<T> {
    const result = this.tail.then(fn);
    this.tail = result.then(
      () => undefined,
      () => undefined,
    );
    return result;
  }
}

/** Push-side never blocks; `next()` waits for an item, a timeout or an abort. */
export class AsyncQueue<T> {
  private items: T[] = [];
  private waiter: ((v: T | undefined) => void) | undefined;

  push(item: T): void {
    if (this.waiter) {
      const w = this.waiter;
      this.waiter = undefined;
      w(item);
    } else {
      this.items.push(item);
    }
  }

  get size(): number {
    return this.items.length;
  }

  /** Resolves with the next item, or undefined after `timeoutMs` or when `signal` aborts. */
  next(timeoutMs: number, signal?: AbortSignal): Promise<T | undefined> {
    if (this.items.length) return Promise.resolve(this.items.shift());
    if (signal?.aborted) return Promise.resolve(undefined);
    return new Promise((resolve) => {
      const done = (v: T | undefined) => {
        clearTimeout(timer);
        signal?.removeEventListener("abort", onAbort);
        this.waiter = undefined;
        resolve(v);
      };
      const onAbort = () => done(undefined);
      const timer = setTimeout(() => done(undefined), timeoutMs);
      signal?.addEventListener("abort", onAbort, { once: true });
      this.waiter = done;
    });
  }
}
