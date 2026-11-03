<script setup lang="ts">
// Markdown table. Columns whose cells are all short (numbers, dates, ids) never wrap; columns of prose get a
// readable minimum width and wrap at spaces. A table wider than the message scrolls inside its frame.
const root = ref<HTMLElement | null>(null)

const SHORT = 24

function layout() {
  const table = root.value?.querySelector('table')
  if (!table) return
  const rows = [...table.rows]
  const cols = Math.max(0, ...rows.map(r => r.cells.length))
  for (let c = 0; c < cols; c++) {
    const cells = rows.map(r => r.cells[c]).filter((x): x is HTMLTableCellElement => !!x)
    const short = cells.every(x => (x.textContent ?? '').trim().length <= SHORT)
    for (const x of cells) x.dataset.col = short ? 'short' : 'text'
  }
}

onMounted(layout)
onUpdated(layout)
</script>

<template>
  <div
    ref="root"
    data-md-table
  >
    <table><slot /></table>
  </div>
</template>
