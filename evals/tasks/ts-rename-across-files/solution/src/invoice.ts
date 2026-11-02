import type { LineItem } from './types.ts';
import * as pricing from './pricing.ts';

export interface Invoice {
  lines: string[];
  total: string;
}

export function buildInvoice(items: LineItem[], taxRate: number): Invoice {
  const lines = items.map(
    (it) => `${it.sku} x${it.quantity} ${pricing.formatCents(it.unitPrice * it.quantity)}`,
  );
  return { lines, total: pricing.formatCents(pricing.computeOrderTotal(items, taxRate)) };
}
