import type { LineItem } from './types.ts';
import { calcTotal, formatCents } from './pricing.ts';

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

  // Delegates to calcTotal so the cart and invoices always agree.
  total(): number {
    return calcTotal(this.items, this.taxRate);
  }

  label(): string {
    return formatCents(this.total());
  }
}
