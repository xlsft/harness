import type { LineItem } from './types.ts';
import { computeOrderTotal, formatCents } from './pricing.ts';

export class Cart {
  items: LineItem[] = [];
  taxRate: number;

  constructor(taxRate = 0) {
    this.taxRate = taxRate;
  }

  add(item: LineItem): void {
    const existing = this.items.find((i) => i.sku === item.sku);
    if (existing) existing.quantity += item.quantity;
    else this.items.push({ ...item });
  }

  // Delegates to computeOrderTotal so the cart and invoices always agree.
  total(): number {
    return computeOrderTotal(this.items, this.taxRate);
  }

  label(): string {
    return formatCents(this.total());
  }
}
