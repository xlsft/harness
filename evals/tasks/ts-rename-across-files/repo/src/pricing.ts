import type { LineItem } from './types.ts';

/**
 * calcTotal sums line items and applies tax. Returns cents, rounded half up.
 */
export function calcTotal(items: LineItem[], taxRate: number): number {
  const subtotal = items.reduce((sum, it) => sum + it.unitPrice * it.quantity, 0);
  return Math.round(subtotal * (1 + taxRate));
}

export function formatCents(cents: number): string {
  return `$${(cents / 100).toFixed(2)}`;
}
