<script setup lang="ts">
import { computed, useId } from 'vue'
import { calculateBeichenPriceTable, type BeichenPriceTableInput } from '../utils/priceCalculation'

const props = defineProps<BeichenPriceTableInput>()
const table = computed(() => calculateBeichenPriceTable(props))
const headingId = useId()
const columns = ['base', 'multiplier', 'settlement', 'student'] as const

const sourceLabels = {
  website: '官网正式价',
  derived: '历史流水反推价，待核对',
  unlisted: '未列报价',
}

const columnLabels = computed(() => ({
  base: table.value.source === 'derived' ? '反推基础价' : '官网价',
  multiplier: '分组倍率',
  settlement: '结算价',
  student: '学生实价',
}))
</script>

<template>
  <section class="beichen-price-quote" :data-source="table.source">
    <header :id="headingId" class="quote-heading">
      <strong>{{ sourceLabels[table.source] }}</strong>
      <small>
        采集时间
        <time v-if="table.observedLabel && table.observedAt" :datetime="table.observedAt">{{ table.observedLabel }}（北京时间）</time>
        <template v-else>未记录</template>
      </small>
      <small>{{ table.unitLabel }}</small>
    </header>
    <div class="quote-scroll" role="region" aria-label="北辰报价表" tabindex="0">
      <table class="quote-table" :aria-labelledby="headingId">
        <thead>
          <tr>
            <th scope="col">项目</th>
            <th v-for="column in columns" :key="column" scope="col" class="quote-number">{{ columnLabels[column] }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in table.rows" :key="row.key" :data-key="row.key">
            <th scope="row">{{ row.label }}</th>
            <td v-for="column in columns" :key="column" class="quote-number">
              <template v-if="column === 'multiplier'">{{ table.groupMultiplierLabel ?? '待同步' }}</template>
              <template v-else-if="row[column]">
                <strong>{{ row[column].credits }} 积分</strong>
                <small>（{{ row[column].yuan }} 元）</small>
              </template>
              <span v-else class="quote-empty">
                {{ column === 'base' ? (row.notApplicable ? '不适用' : '— 未列单价') : '—' }}
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.beichen-price-quote {
  min-width: 0;
  max-width: 100%;
  container-type: inline-size;
}

.quote-heading {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px 12px;
  margin-bottom: 6px;
  font-size: 12px;
}

.quote-heading small, .quote-table small, .quote-empty {
  color: var(--ink-soft);
}

.quote-scroll {
  contain: inline-size;
  overflow-x: auto;
}

.quote-scroll:focus-visible {
  outline: 2px solid var(--primary);
  outline-offset: 2px;
}

/* 在会重新排列列的外层北辰表格中，也保留这张报价表的五列。 */
.quote-table {
  display: table;
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}

.quote-table thead { display: table-header-group; }
.quote-table tbody { display: table-row-group; }
.quote-table tr { display: table-row; }

.quote-table th, .quote-table td {
  display: table-cell;
  width: auto;
  padding: 6px 8px;
  border-bottom: 1px solid var(--line);
  text-align: left;
  white-space: nowrap;
  overflow-wrap: normal;
}

.quote-table .quote-number {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.quote-table small { font-size: 0.92em; }

/* 窄卡片内改为每个档位一张小表，避免学生实价被横向滚动区裁掉。 */
@container (max-width: 30rem) {
  .quote-scroll { overflow-x: visible; }

  .quote-table,
  .quote-table tbody,
  .quote-table tr,
  .quote-table th,
  .quote-table td {
    display: block;
    width: auto;
  }

  .quote-table thead { display: none; }
  .quote-table tbody { display: grid; gap: 8px; }

  .quote-table tr {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    overflow: hidden;
    border: 1px solid var(--line);
    border-radius: 10px;
  }

  .quote-table th[scope="row"] {
    grid-column: 1 / -1;
    padding: 7px 9px;
    border-bottom: 1px solid var(--line);
    background: rgba(237, 240, 247, .55);
    white-space: normal;
  }

  .quote-table td {
    min-width: 0;
    padding: 7px 9px;
    border-bottom: 0;
    text-align: left;
    white-space: normal;
  }

  .quote-table td:nth-child(2),
  .quote-table td:nth-child(3) { border-bottom: 1px solid var(--line); }
  .quote-table td:nth-child(3),
  .quote-table td:nth-child(5) { border-left: 1px solid var(--line); }

  .quote-table td::before {
    display: block;
    margin-bottom: 3px;
    color: var(--ink-soft);
    font-size: 10px;
    font-weight: 650;
    line-height: 1.4;
  }

  .quote-table td:nth-child(2)::before { content: '官网价'; }
  .beichen-price-quote[data-source="derived"] .quote-table td:nth-child(2)::before { content: '反推基础价'; }
  .quote-table td:nth-child(3)::before { content: '分组倍率'; }
  .quote-table td:nth-child(4)::before { content: '结算价'; }
  .quote-table td:nth-child(5)::before { content: '学生实价'; }

  .quote-table td strong,
  .quote-table td small { display: block; white-space: nowrap; }
}
</style>
