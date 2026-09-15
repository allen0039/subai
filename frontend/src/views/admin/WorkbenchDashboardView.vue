<template>
  <WorkbenchLayout>
    <div class="workbench-dashboard">
      <header class="mb-5 flex flex-wrap items-end justify-between gap-4">
        <div>
          <p class="mb-1 text-xs font-semibold uppercase tracking-[0.16em] text-[#4dcbb9]">管理控制台</p>
          <h1 class="text-2xl font-bold tracking-tight text-[#eaf0f6]">运行总览</h1>
          <p class="mt-1 text-sm text-[#9eafbf]">流量、额度与渠道健康，一屏掌握</p>
        </div>
        <div class="flex items-center gap-2">
          <select v-model="range" class="workbench-select" aria-label="数据时间范围" @change="loadDashboard">
            <option value="90m">近 90 分钟</option>
            <option value="24h">近 24 小时</option>
            <option value="7d">近 7 天</option>
            <option value="30d">近 30 天</option>
          </select>
          <button class="workbench-icon-button" :disabled="loading" title="刷新数据" @click="loadDashboard">
            <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
          </button>
        </div>
      </header>

      <div v-if="loadError" class="workbench-notice mb-4" role="status">
        <Icon name="exclamationCircle" size="sm" />
        <span>{{ loadError }}</span>
        <button type="button" class="ml-auto text-xs font-semibold underline" @click="loadDashboard">重试</button>
      </div>

      <section class="workbench-status-strip mb-5" aria-label="关键运行指标">
        <div class="workbench-status-item">
          <span class="status-dot" :class="healthClass"></span>
          <div><span>运行健康</span><strong>{{ healthLabel }}</strong></div>
        </div>
        <div class="workbench-status-item"><div><span>区间请求</span><strong>{{ formatNumber(ops?.request_count_total) }}</strong></div></div>
        <div class="workbench-status-item"><div><span>成功率</span><strong>{{ successRate }}</strong></div></div>
        <div class="workbench-status-item"><div><span>请求耗时 P95</span><strong>{{ formatDuration(ops?.duration?.p95_ms) }}</strong></div></div>
        <div class="workbench-status-item"><div><span>今日实际扣费</span><strong>${{ formatCost(stats?.today_actual_cost) }}</strong></div></div>
      </section>

      <div class="grid gap-5 xl:grid-cols-[minmax(0,1fr)_340px]">
        <div class="min-w-0 space-y-5">
          <section class="workbench-panel overflow-hidden">
            <div class="workbench-panel-header">
              <div>
                <h2>请求流量</h2>
                <p>{{ rangeLabel }} · 请求量与 Token 消耗</p>
              </div>
              <span v-if="updatedAt" class="text-xs text-[#9eafbf]">更新于 {{ formatDateTime(updatedAt) }}</span>
            </div>
            <div v-if="loading && !throughput.length" class="flex h-[300px] items-center justify-center text-sm text-[#9eafbf]">正在加载运行数据…</div>
            <div v-else-if="trafficPoints.length" class="workbench-chart-wrap">
              <svg class="workbench-chart" viewBox="0 0 1000 300" preserveAspectRatio="none" role="img" aria-label="请求流量趋势图">
                <line v-for="n in 5" :key="n" x1="0" :y1="n * 50" x2="1000" :y2="n * 50" class="chart-grid" />
                <path :d="requestAreaPath" class="chart-area" />
                <path :d="requestLinePath" class="chart-request-line" />
                <path :d="tokenLinePath" class="chart-token-line" />
              </svg>
              <div class="workbench-chart-legend"><span><i class="legend-request"></i>请求量</span><span><i class="legend-token"></i>{{ trafficMetricLabel }}</span></div>
            </div>
            <div v-else class="flex h-[300px] items-center justify-center text-sm text-[#9eafbf]">当前范围没有流量数据</div>
          </section>

          <div class="grid gap-5 lg:grid-cols-[minmax(0,1.12fr)_minmax(0,0.88fr)]">
            <section class="workbench-panel">
              <div class="workbench-panel-header"><div><h2>平台负载</h2><p>按平台聚合的真实请求与 Token</p></div></div>
              <div v-if="platformBreakdown.length" class="divide-y divide-[#2c3949]">
                <button v-for="item in platformBreakdown" :key="item.platform" class="workbench-platform-row" @click="selectPlatform(item.platform)">
                  <span class="truncate font-medium text-[#eaf0f6]">{{ item.platform }}</span>
                  <span class="text-[#9eafbf]">{{ formatNumber(item.request_count) }} 请求</span>
                  <span class="w-24 text-right font-mono text-[#4dcbb9]">{{ formatTokens(item.token_consumed) }}</span>
                  <Icon name="chevronRight" size="sm" class="text-[#9eafbf]" />
                </button>
              </div>
              <p v-else class="py-8 text-center text-sm text-[#9eafbf]">暂无可聚合的平台数据</p>
            </section>

            <section class="workbench-panel overflow-hidden">
              <div class="workbench-panel-header"><div><h2>渠道健康时间线</h2><p>按真实请求的 {{ matrixGroupLabel }} 维度聚合</p></div></div>
              <div v-if="matrix?.items?.length" class="max-h-[265px] overflow-auto px-4 pb-4">
                <button v-for="row in matrix.items.slice(0, 8)" :key="matrixKey(row)" class="workbench-matrix-row" :class="isSelected(row) ? 'is-selected' : ''" @click="selectMatrixRow(row)">
                  <span class="min-w-0 truncate text-left text-sm font-medium text-[#eaf0f6]">{{ matrixLabel(row) }}</span>
                  <span class="health-cells" aria-hidden="true"><i v-for="bucket in row.buckets" :key="bucket.bucket_start" :class="healthCellClass(bucket.health.overall)"></i></span>
                </button>
              </div>
              <p v-else class="px-4 py-8 text-center text-sm text-[#9eafbf]">监控数据不可用或当前范围没有样本</p>
            </section>
          </div>

          <section class="workbench-panel overflow-hidden">
            <div class="workbench-panel-header"><div><h2>最近请求</h2><p>仅展示可访问的请求元数据</p></div><router-link to="/admin/ops" class="workbench-text-link">查看运行分析</router-link></div>
            <div class="overflow-x-auto">
              <table class="workbench-table">
                <thead><tr><th>时间</th><th>平台 / 模型</th><th>状态</th><th>耗时</th><th>请求 ID</th></tr></thead>
                <tbody>
                  <tr v-for="request in recentRequests" :key="request.request_id"><td>{{ formatTime(request.created_at) }}</td><td><span class="text-[#eaf0f6]">{{ request.platform || '—' }}</span><span class="ml-1 text-[#9eafbf]">{{ request.model || '' }}</span></td><td><span class="request-status" :class="request.kind === 'success' ? 'is-success' : 'is-error'">{{ request.kind === 'success' ? '成功' : '失败' }}</span></td><td>{{ formatDuration(request.duration_ms) }}</td><td class="font-mono text-xs text-[#9eafbf]">{{ maskRequestId(request.request_id) }}</td></tr>
                  <tr v-if="!recentRequests.length"><td colspan="5" class="py-7 text-center text-[#9eafbf]">当前范围没有可展示的请求</td></tr>
                </tbody>
              </table>
            </div>
          </section>
        </div>

        <aside class="workbench-inspector xl:sticky xl:top-[88px] xl:self-start" aria-label="运行详情">
          <div class="workbench-inspector-header"><div><p>运行详情</p><h2>{{ selectedLabel }}</h2></div><button v-if="selected" class="workbench-icon-button" title="清除选择" @click="selected = null"><Icon name="x" size="sm" /></button></div>
          <template v-if="selected">
            <div class="workbench-inspector-state"><span class="status-dot" :class="selectedStateClass"></span>{{ selectedState }}</div>
            <div class="workbench-metric-grid">
              <div><span>成功率</span><strong>{{ selectedSuccessRate }}</strong></div>
              <div><span>请求耗时 P95</span><strong>{{ formatDuration(selected.metrics.duration.p95_ms) }}</strong></div>
              <div><span>首字延迟 P95</span><strong>{{ formatDuration(selected.metrics.ttft.p95_ms) }}</strong></div>
              <div><span>请求数</span><strong>{{ formatNumber(selected.metrics.request_count) }}</strong></div>
            </div>
            <div class="mt-5 border-t border-[#2c3949] pt-4"><p class="inspector-section-label">当前选择</p><dl class="space-y-2 text-sm"><div><dt>平台</dt><dd>{{ selected.platform }}</dd></div><div v-if="selected.group_name || selected.group_id"><dt>分组</dt><dd>{{ selected.group_name || `#${selected.group_id}` }}</dd></div><div v-if="selected.model"><dt>模型</dt><dd>{{ selected.model }}</dd></div><div><dt>覆盖状态</dt><dd>{{ matrix?.coverage.coverage_complete ? '已覆盖所选范围' : '数据仍在聚合或回填' }}</dd></div></dl></div>
            <router-link :to="opsLink" class="workbench-primary-action"><Icon name="chart" size="sm" />查看运行分析</router-link>
            <p class="mt-3 text-xs leading-5 text-[#9eafbf]">当前详情按平台、分组或模型聚合。未确认到具体账户时，不展示账户额度或直接测试操作。</p>
          </template>
          <div v-else class="flex min-h-[250px] flex-col items-center justify-center px-5 text-center"><Icon name="grid" size="lg" class="mb-3 text-[#709cf8]" /><p class="text-sm font-semibold text-[#eaf0f6]">选择一条健康记录</p><p class="mt-1 text-xs leading-5 text-[#9eafbf]">查看同一平台、分组或模型维度的真实运行指标。</p></div>
        </aside>
      </div>
    </div>
  </WorkbenchLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import WorkbenchLayout from '@/components/layout/WorkbenchLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import { opsAPI, type OpsDashboardOverview, type OpsRequestDetail, type OpsThroughputTrendResponse } from '@/api/admin/ops'
import { getMatrix, getSnapshot, type HealthState, type MonitorMatrixResponse, type MonitorMatrixRow, type MonitorSnapshot } from '@/api/channelMonitorV2'
import type { DashboardStats } from '@/types'

type Range = '90m' | '24h' | '7d' | '30d'
const router = useRouter()
const range = ref<Range>('90m')
const loading = ref(false)
const loadError = ref('')
const stats = ref<DashboardStats | null>(null)
const ops = ref<OpsDashboardOverview | null>(null)
const throughputData = ref<OpsThroughputTrendResponse | null>(null)
const monitorSnapshot = ref<MonitorSnapshot | null>(null)
const matrix = ref<MonitorMatrixResponse | null>(null)
const recentRequests = ref<OpsRequestDetail[]>([])
const selected = ref<MonitorMatrixRow | null>(null)
const updatedAt = ref<string | null>(null)
let controller: AbortController | null = null
let requestSequence = 0

const throughput = computed(() => throughputData.value?.points || [])
const trafficPoints = computed(() => {
  if (throughput.value.length) {
    return throughput.value.map((point) => ({ requestCount: point.request_count, tokenMetric: point.tps }))
  }
  return (monitorSnapshot.value?.trend || []).map((point) => ({ requestCount: point.metrics.request_count, tokenMetric: point.metrics.tpm }))
})
const trafficMetricLabel = computed(() => throughput.value.length ? 'Token / 秒' : 'Token / 分钟')
const platformBreakdown = computed(() => throughputData.value?.by_platform || [])
const rangeLabel = computed(() => ({ '90m': '近 90 分钟', '24h': '近 24 小时', '7d': '近 7 天', '30d': '近 30 天' }[range.value]))
const matrixGroupLabel = computed(() => ({ platform: '平台', platform_group: '平台与分组', platform_model: '平台与模型', platform_group_model: '平台、分组与模型' }[matrix.value?.group_by || 'platform_group_model']))
const healthLabel = computed(() => monitorSnapshot.value?.health.overall === 'healthy' ? '运行正常' : monitorSnapshot.value?.health.overall === 'warning' ? '需要关注' : monitorSnapshot.value?.health.overall === 'critical' ? '异常' : '数据待确认')
const healthClass = computed(() => healthCellClass(monitorSnapshot.value?.health.overall || 'unknown'))
const successRate = computed(() => formatPercent(1 - (ops.value?.error_rate ?? 0)))
const selectedLabel = computed(() => selected.value ? matrixLabel(selected.value) : '未选择维度')
const selectedState = computed(() => selected.value ? healthText(selected.value.health.overall) : '')
const selectedStateClass = computed(() => healthCellClass(selected.value?.health.overall || 'unknown'))
const selectedSuccessRate = computed(() => selected.value ? formatPercent(1 - (selected.value.metrics.error_rate || 0)) : '—')
const opsLink = computed(() => ({ path: '/admin/ops', query: selected.value?.platform ? { platform: selected.value.platform } : {} }))

function pointPath(values: number[]) {
  if (!values.length) return ''
  const max = Math.max(...values, 1)
  const width = 1000 / Math.max(values.length - 1, 1)
  return values.map((value, index) => `${index === 0 ? 'M' : 'L'} ${(index * width).toFixed(2)} ${(278 - (value / max) * 238).toFixed(2)}`).join(' ')
}
const requestLinePath = computed(() => pointPath(trafficPoints.value.map((point) => point.requestCount)))
const tokenLinePath = computed(() => pointPath(trafficPoints.value.map((point) => point.tokenMetric)))
const requestAreaPath = computed(() => requestLinePath.value ? `${requestLinePath.value} L 1000 278 L 0 278 Z` : '')

async function loadDashboard() {
  controller?.abort()
  controller = new AbortController()
  const sequence = ++requestSequence
  loading.value = true
  loadError.value = ''
  selected.value = null
  try {
    const opsRange = range.value === '90m' ? '1h' : range.value === '24h' ? '24h' : undefined
    const [dashboardResult, opsResult, throughputResult, snapshotResult, matrixResult, requestsResult] = await Promise.allSettled([
      adminAPI.dashboard.getStats(),
      opsRange ? opsAPI.getDashboardOverview({ time_range: opsRange }, { signal: controller.signal }) : Promise.resolve(null),
      opsRange ? opsAPI.getThroughputTrend({ time_range: opsRange }, { signal: controller.signal }) : Promise.resolve(null),
      getSnapshot({ range: range.value, platforms: [], groupIds: [], models: [] }, true, controller.signal),
      getMatrix({ range: range.value, platforms: [], groupIds: [], models: [] }, 'platform_group_model', true, controller.signal),
      opsRange ? opsAPI.listRequestDetails({ time_range: opsRange, page: 1, page_size: 5 }) : Promise.resolve(null),
    ])
    if (sequence !== requestSequence) return
    if (dashboardResult.status === 'fulfilled') stats.value = dashboardResult.value
    if (opsResult.status === 'fulfilled') ops.value = opsResult.value
    if (throughputResult.status === 'fulfilled') throughputData.value = throughputResult.value
    if (snapshotResult.status === 'fulfilled') monitorSnapshot.value = snapshotResult.value
    if (matrixResult.status === 'fulfilled') matrix.value = matrixResult.value
    if (requestsResult.status === 'fulfilled' && requestsResult.value) recentRequests.value = requestsResult.value.items || []
    const failures = [dashboardResult, opsResult, throughputResult, snapshotResult, matrixResult, requestsResult].filter((result) => result.status === 'rejected')
    if (failures.length === 6) loadError.value = '运行数据暂时不可用，请稍后重试。'
    else if (failures.length) loadError.value = '部分运行数据暂时不可用，已保留其余可用数据。'
    updatedAt.value = new Date().toISOString()
  } catch (error: any) {
    if (error?.code !== 'ERR_CANCELED' && sequence === requestSequence) loadError.value = '加载运行数据时发生错误。'
  } finally {
    if (sequence === requestSequence) loading.value = false
  }
}

function selectPlatform(platform: string) { void router.push({ path: '/admin/ops', query: { platform } }) }
function selectMatrixRow(row: MonitorMatrixRow) { selected.value = row }
function matrixKey(row: MonitorMatrixRow) { return `${row.platform}:${row.group_id || 0}:${row.model || ''}` }
function isSelected(row: MonitorMatrixRow) { return matrixKey(selected.value || ({} as MonitorMatrixRow)) === matrixKey(row) }
function matrixLabel(row: MonitorMatrixRow) { return [row.platform, row.group_name || (row.group_id ? `#${row.group_id}` : ''), row.model].filter(Boolean).join(' / ') }
function healthText(state: HealthState) { return state === 'healthy' ? '运行正常' : state === 'warning' ? '需要关注' : state === 'critical' ? '异常' : '数据待确认' }
function healthCellClass(state: HealthState) { return `health-${state}` }
function formatPercent(value: number) { return Number.isFinite(value) ? `${(Math.max(0, Math.min(1, value)) * 100).toFixed(2)}%` : '—' }
function formatNumber(value: number | null | undefined) { return Number(value || 0).toLocaleString() }
function formatTokens(value: number | null | undefined) { const safe = Number(value || 0); return safe >= 1_000_000 ? `${(safe / 1_000_000).toFixed(2)}M` : safe >= 1_000 ? `${(safe / 1_000).toFixed(1)}K` : safe.toLocaleString() }
function formatCost(value: number | null | undefined) { return Number(value || 0).toFixed(2) }
function formatDuration(value: number | null | undefined) { const safe = Number(value); return Number.isFinite(safe) ? safe >= 1000 ? `${(safe / 1000).toFixed(2)}s` : `${Math.round(safe)}ms` : '—' }
function formatDateTime(value: string) { return new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(new Date(value)) }
function formatTime(value: string) { return new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(new Date(value)) }
function maskRequestId(value: string) { return value.length > 12 ? `${value.slice(0, 8)}…${value.slice(-4)}` : value }

onMounted(loadDashboard)
onBeforeUnmount(() => controller?.abort())
</script>

<style scoped>
.workbench-dashboard { --panel: #1a2431; --border: #2c3949; --text: #eaf0f6; --muted: #9eafbf; --teal: #4dcbb9; }
.workbench-select,.workbench-icon-button { border: 1px solid var(--border); border-radius: 7px; background: var(--panel); color: var(--text); }
.workbench-select { min-height: 36px; padding: 0 30px 0 11px; font-size: 13px; }.workbench-icon-button { display: inline-flex; height: 36px; width: 36px; align-items: center; justify-content: center; transition: background-color 150ms ease; }.workbench-icon-button:hover:not(:disabled){ background:#263548 }.workbench-icon-button:disabled{ opacity:.5 }
.workbench-notice,.workbench-status-strip,.workbench-panel,.workbench-inspector { border:1px solid var(--border); background:var(--panel); box-shadow:0 12px 28px rgba(0,0,0,.12); }.workbench-notice { display:flex; gap:8px; align-items:center; border-color:#8a6434; padding:10px 12px; border-radius:7px; color:#f5c46a; font-size:13px }
.workbench-status-strip { display:grid; grid-template-columns:repeat(5,minmax(0,1fr)); border-radius:8px; overflow:hidden }.workbench-status-item{ display:flex; align-items:center; gap:9px; min-height:76px; padding:14px 16px; border-right:1px solid var(--border) }.workbench-status-item:last-child{border-right:0}.workbench-status-item span:not(.status-dot){display:block;font-size:12px;color:var(--muted)}.workbench-status-item strong{display:block;margin-top:3px;font-size:20px;font-variant-numeric:tabular-nums;color:var(--text)}.status-dot{display:block;flex:0 0 auto;width:9px;height:9px;border-radius:50%}.health-healthy{background:#4dcbb9}.health-warning{background:#f5b75b}.health-critical{background:#ef6d62}.health-unknown{background:#617386}
.workbench-panel,.workbench-inspector { border-radius:8px; }.workbench-panel-header{display:flex;align-items:flex-start;justify-content:space-between;gap:12px;padding:16px;border-bottom:1px solid var(--border)}.workbench-panel-header h2,.workbench-inspector h2{font-size:15px;font-weight:700;color:var(--text)}.workbench-panel-header p,.workbench-inspector-header p{margin-top:3px;font-size:12px;color:var(--muted)}.workbench-chart-wrap{padding:14px 16px 10px}.workbench-chart{height:286px;width:100%;overflow:visible}.chart-grid{stroke:#2c3949;stroke-width:1}.chart-area{fill:rgba(77,203,185,.12)}.chart-request-line{fill:none;stroke:#4dcbb9;stroke-width:4;vector-effect:non-scaling-stroke}.chart-token-line{fill:none;stroke:#709cf8;stroke-width:3;vector-effect:non-scaling-stroke}.workbench-chart-legend{display:flex;gap:16px;font-size:12px;color:var(--muted)}.workbench-chart-legend span{display:flex;align-items:center;gap:6px}.workbench-chart-legend i{display:block;width:8px;height:8px;border-radius:50%}.legend-request{background:#4dcbb9}.legend-token{background:#709cf8}
.workbench-platform-row{display:grid;width:100%;grid-template-columns:minmax(0,1fr) auto 96px auto;gap:12px;align-items:center;padding:13px 16px;text-align:left;font-size:13px;transition:background-color 150ms ease}.workbench-platform-row:hover{background:#202d3c}.workbench-matrix-row{display:grid;width:100%;grid-template-columns:minmax(120px,1fr) 150px;gap:10px;align-items:center;border-radius:6px;padding:9px 8px;transition:background-color 150ms ease}.workbench-matrix-row:hover,.workbench-matrix-row.is-selected{background:#213554}.health-cells{display:grid;grid-template-columns:repeat(auto-fit,minmax(5px,1fr));gap:2px;height:14px}.health-cells i{border-radius:2px}.health-cells .health-unknown{opacity:.55}
.workbench-table{width:100%;min-width:650px;font-size:13px}.workbench-table th{padding:10px 16px;background:#17212d;text-align:left;font-size:11px;font-weight:600;color:var(--muted)}.workbench-table td{border-top:1px solid var(--border);padding:11px 16px;color:var(--muted)}.request-status{display:inline-flex;border-radius:999px;padding:2px 7px;font-size:11px;font-weight:700}.request-status.is-success{background:rgba(77,203,185,.15);color:#6de1d0}.request-status.is-error{background:rgba(239,109,98,.14);color:#ff9a91}.workbench-text-link{font-size:12px;font-weight:700;color:#95b4ff}
.workbench-inspector{overflow:hidden}.workbench-inspector-header{display:flex;justify-content:space-between;gap:12px;padding:16px;border-bottom:1px solid var(--border)}.workbench-inspector-state{display:flex;align-items:center;gap:7px;margin:16px;color:var(--text);font-size:13px;font-weight:700}.workbench-metric-grid{display:grid;grid-template-columns:1fr 1fr;gap:1px;border-top:1px solid var(--border);border-bottom:1px solid var(--border);background:var(--border)}.workbench-metric-grid div{background:var(--panel);padding:13px 16px}.workbench-metric-grid span,.inspector-section-label{display:block;font-size:11px;color:var(--muted)}.workbench-metric-grid strong{display:block;margin-top:5px;color:var(--text);font-size:17px;font-variant-numeric:tabular-nums}.workbench-inspector dl{padding:0 16px}.workbench-inspector dl div{display:flex;justify-content:space-between;gap:16px}.workbench-inspector dt{color:var(--muted)}.workbench-inspector dd{max-width:65%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--text)}.workbench-primary-action{display:flex;align-items:center;justify-content:center;gap:8px;margin:20px 16px 0;border-radius:7px;background:#5f8fff;padding:10px;color:#0d1825;font-size:13px;font-weight:800}.inspector-section-label{padding:0 16px 10px;font-weight:700;text-transform:uppercase;letter-spacing:.08em}
@media(max-width:1279px){.workbench-status-strip{grid-template-columns:repeat(3,minmax(0,1fr))}.workbench-status-item:nth-child(3){border-right:0}.workbench-status-item:nth-child(n+4){border-top:1px solid var(--border)}}@media(max-width:640px){.workbench-status-strip{grid-template-columns:repeat(2,minmax(0,1fr))}.workbench-status-item{min-height:68px;padding:11px}.workbench-status-item:nth-child(2n){border-right:0}.workbench-status-item:nth-child(n+3){border-top:1px solid var(--border)}.workbench-status-item strong{font-size:17px}.workbench-chart{height:230px}.workbench-platform-row{grid-template-columns:minmax(0,1fr) auto auto}.workbench-platform-row span:nth-child(2){display:none}}
</style>
