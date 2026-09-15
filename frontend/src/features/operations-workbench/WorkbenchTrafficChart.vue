<template><div class="h-[280px]"><Line :data="data" :options="options" /></div></template>
<script setup lang="ts">
import { computed } from 'vue'
import { useMutationObserver } from '@vueuse/core'
import { ref } from 'vue'
import { Chart as ChartJS, CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend } from 'chart.js'
import { Line } from 'vue-chartjs'
ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend)
const props = defineProps<{ points: { requestCount: number; tokenMetric: number }[]; labels: string[]; tokenLabel: string }>()
const dark = ref(document.documentElement.classList.contains('dark'))
useMutationObserver(document.documentElement, () => { dark.value = document.documentElement.classList.contains('dark') }, { attributes: true, attributeFilter: ['class'] })
const data = computed(() => ({
  labels: props.labels.map(label => label.replace('T', ' ').replace(/:\d\d(?:\.\d+)?Z$/, '')),
  datasets: [
    { label: '请求量', data: props.points.map(p => p.requestCount), borderColor: dark.value ? '#4dcbb9' : '#168c7e', yAxisID: 'requests', pointRadius: 0, borderWidth: 2 },
    { label: props.tokenLabel, data: props.points.map(p => p.tokenMetric), borderColor: dark.value ? '#95b4ff' : '#315dc6', yAxisID: 'tokens', pointRadius: 0, borderWidth: 2 },
  ],
}))
const options = computed(() => {
  const color = dark.value ? '#9eafbf' : '#52647b'
  return {
    responsive: true, maintainAspectRatio: false,
    interaction: { mode: 'index' as const, intersect: false },
    plugins: { legend: { labels: { color, usePointStyle: true } } },
    scales: {
      x: { ticks: { color, maxTicksLimit: 6 }, grid: { display: false } },
      requests: { position: 'left' as const, beginAtZero: true, ticks: { color }, title: { display: true, text: '请求量', color } },
      tokens: { position: 'right' as const, beginAtZero: true, ticks: { color }, grid: { drawOnChartArea: false }, title: { display: true, text: props.tokenLabel, color } },
    },
  }
})
</script>
