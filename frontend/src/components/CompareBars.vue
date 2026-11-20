<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { BarChart } from 'echarts/charts'
import { AriaComponent, GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { SVGRenderer } from 'echarts/renderers'

echarts.use([BarChart, GridComponent, TooltipComponent, LegendComponent, AriaComponent, SVGRenderer])

const props = withDefaults(defineProps<{
  categories: string[]
  left: Array<number | null>
  right: Array<number | null>
  leftLabel: string
  rightLabel: string
  valueFormatter?: (value: number) => string
  unit?: string
}>(), { unit: 'Token' })

const root = ref<HTMLElement | null>(null)
const hasData = computed(() => props.categories.length > 0 && [...props.left, ...props.right]
  .some(value => value != null && Number.isFinite(value)))
const height = computed(() => `${Math.max(280, props.categories.length * 50 + 90)}px`)
const description = computed(() => `${props.leftLabel}与${props.rightLabel}对比（${props.unit}）`)
let chart: echarts.ECharts | undefined
let observer: ResizeObserver | undefined
let unmounted = false

function valid(value: number | null | undefined): number | null {
  return value != null && Number.isFinite(value) ? value : null
}

function display(value: number | null | undefined) {
  const number = valid(value)
  return number == null ? '—' : (props.valueFormatter?.(number) ?? number.toLocaleString('zh-CN'))
}

function resize() { chart?.resize() }

function dispose() {
  observer?.disconnect()
  observer = undefined
  chart?.dispose()
  chart = undefined
}

function render() {
  if (unmounted) return
  if (!root.value || !hasData.value) {
    dispose()
    return
  }
  if (!chart) {
    chart = echarts.init(root.value, undefined, { renderer: 'svg' })
    if (typeof ResizeObserver !== 'undefined') {
      observer = new ResizeObserver(resize)
      observer.observe(root.value)
    }
  }
  chart.setOption({
    animation: !window.matchMedia('(prefers-reduced-motion: reduce)').matches,
    aria: { enabled: true, description: description.value },
    color: ['#0071e3', '#168473'],
    grid: { left: 8, right: 28, top: 48, bottom: 30, containLabel: true },
    legend: { top: 0, left: 0, type: 'scroll' },
    tooltip: {
      trigger: 'axis', confine: true, axisPointer: { type: 'shadow' },
      valueFormatter: (value: unknown) => `${display(typeof value === 'number' ? value : null)} ${props.unit}`,
    },
    xAxis: {
      type: 'value', name: props.unit, nameLocation: 'middle', nameGap: 24,
      axisLabel: { formatter: (value: number) => display(value), hideOverlap: true },
      splitLine: { lineStyle: { color: '#edf0f5' } },
    },
    yAxis: {
      type: 'category', inverse: true, data: props.categories,
      axisLine: { show: false }, axisTick: { show: false },
      axisLabel: {
        width: 110, overflow: 'truncate',
        formatter: (value: string) => value.length > 18 ? `${value.slice(0, 17)}…` : value,
      },
    },
    series: [
      { name: props.leftLabel, type: 'bar', barMaxWidth: 15, data: props.categories.map((_, index) => valid(props.left[index])) },
      { name: props.rightLabel, type: 'bar', barMaxWidth: 15, data: props.categories.map((_, index) => valid(props.right[index])) },
    ],
  }, true)
  resize()
}

onMounted(() => {
  render()
  window.addEventListener('resize', resize)
})
watch(() => [props.categories, props.left, props.right, props.leftLabel, props.rightLabel, props.valueFormatter, props.unit], async () => {
  await nextTick()
  render()
}, { deep: true })
onBeforeUnmount(() => {
  unmounted = true
  window.removeEventListener('resize', resize)
  dispose()
})
</script>

<template>
  <div class="compare-bars">
    <div v-if="hasData" class="bars-viewport">
      <div ref="root" class="bars-chart" :style="{ height }" role="img" :aria-label="description" />
    </div>
    <p v-else class="beichen-empty" role="status">当前范围暂无可比较的数据。</p>
    <div class="sr-only">
    <table class="bars-data">
      <caption>{{ description }}数据表</caption>
      <thead><tr><th scope="col">模型</th><th scope="col">{{ leftLabel }}（{{ unit }}）</th><th scope="col">{{ rightLabel }}（{{ unit }}）</th></tr></thead>
      <tbody>
        <tr v-for="(category, index) in categories" :key="category">
          <th scope="row">{{ category }}</th><td>{{ display(left[index]) }}</td><td>{{ display(right[index]) }}</td>
        </tr>
      </tbody>
    </table>
    </div>
  </div>
</template>

<style scoped>
.compare-bars, .bars-viewport, .bars-chart { min-width: 0; width: 100%; }
.compare-bars { position: relative; }
.bars-viewport { max-height: 640px; overflow-y: auto; overflow-x: hidden; }
.bars-chart { min-height: 280px; }
</style>
