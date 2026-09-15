<template>
  <div class="workbench-shell min-h-screen bg-[#121923] text-[#eaf0f6]">
    <AppHeader />

    <aside class="workbench-rail fixed bottom-0 left-0 top-16 z-20 hidden w-[76px] flex-col border-r border-[#2c3949] bg-[#121923] py-3 lg:flex">
      <router-link to="/admin/dashboard" class="mb-4 flex items-center justify-center" aria-label="Sub2API">
        <img :src="siteLogo || '/logo.svg'" alt="Sub2API" class="h-8 w-8 rounded-lg object-contain" />
      </router-link>

      <nav class="flex flex-1 flex-col gap-1 px-2" aria-label="管理控制台导航">
        <router-link
          v-for="item in primaryItems"
          :key="item.path"
          :to="item.path"
          class="workbench-rail-link"
          :class="isActive(item.path) ? 'workbench-rail-link-active' : ''"
          :title="item.label"
        >
          <Icon :name="item.icon" size="md" />
          <span>{{ item.label }}</span>
        </router-link>
      </nav>

      <div class="border-t border-[#2c3949] px-2 pt-2">
        <router-link to="/admin/settings" class="workbench-rail-link" :class="isActive('/admin/settings') ? 'workbench-rail-link-active' : ''" title="设置">
          <Icon name="cog" size="md" />
          <span>设置</span>
        </router-link>
      </div>
    </aside>

    <main class="min-h-[calc(100vh-64px)] p-4 md:p-6 lg:ml-[76px] lg:p-6 xl:p-7">
      <slot />
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useAppStore } from '@/stores'
import AppHeader from './AppHeader.vue'
import Icon from '@/components/icons/Icon.vue'

const route = useRoute()
const appStore = useAppStore()
const siteLogo = computed(() => appStore.siteLogo)

const primaryItems = [
  { path: '/admin/dashboard', label: '总览', icon: 'home' },
  { path: '/admin/ops', label: '运行', icon: 'chart' },
  { path: '/admin/accounts', label: '账户', icon: 'server' },
  { path: '/admin/channels', label: '渠道', icon: 'globe' },
  { path: '/admin/usage', label: '用量', icon: 'document' },
  { path: '/admin/audit-logs', label: '审计', icon: 'shield' },
] as const

function isActive(path: string) {
  return route.path === path || (path !== '/admin/dashboard' && route.path.startsWith(`${path}/`))
}
</script>

<style scoped>
.workbench-shell :deep(.glass) {
  background: #121923;
  border-color: #2c3949;
}

.workbench-shell :deep(.glass h1),
.workbench-shell :deep(.glass .text-gray-900) {
  color: #eaf0f6;
}

.workbench-shell :deep(.glass .text-gray-500),
.workbench-shell :deep(.glass .text-gray-600) {
  color: #9eafbf;
}

.workbench-rail-link {
  display: flex;
  min-height: 52px;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 3px;
  border-radius: 7px;
  color: #9eafbf;
  font-size: 11px;
  font-weight: 600;
  transition: color 150ms ease, background-color 150ms ease;
}

.workbench-rail-link:hover { background: #1a2431; color: #eaf0f6; }
.workbench-rail-link-active { background: #213554; color: #95b4ff; }
</style>
