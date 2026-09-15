<template>
  <a class="wb-skip" href="#workbench-content">{{ text('跳至内容', 'Skip to content') }}</a>
  <button v-if="app.mobileOpen" class="wb-mobile-backdrop" :aria-label="t('common.close')" @click="app.setMobileOpen(false)" />
  <aside class="wb-navigation" :class="{ 'is-open': app.mobileOpen }" :aria-label="text('工作台导航', 'Workspace navigation')">
    <div class="wb-rail">
      <router-link :to="auth.isAdmin ? '/admin/dashboard' : '/dashboard'" class="wb-brand" :title="siteName"><img :src="siteLogo || '/logo.svg'" :alt="siteName" /></router-link>
      <nav class="wb-rail-items">
        <button v-for="group in groups" :key="group.id" :class="{ active: activeGroup.id === group.id }" :aria-current="activeGroup.id === group.id ? 'true' : undefined" :title="group.label" @click="openGroup(group)">
          <Icon :name="group.icon" /><span>{{ group.label }}</span>
        </button>
      </nav>
      <button class="wb-theme" :title="isDark ? t('nav.lightMode') : t('nav.darkMode')" :aria-label="isDark ? t('nav.lightMode') : t('nav.darkMode')" @click="$emit('toggle-theme')"><Icon :name="isDark ? 'sun' : 'moon'" /></button>
    </div>
    <div class="wb-section">
      <div class="wb-section-brand"><strong>{{ siteName }}</strong><button class="lg:hidden" :aria-label="t('common.close')" @click="app.setMobileOpen(false)"><Icon name="x" size="sm" /></button></div>
      <p class="wb-space-label">{{ auth.isAdmin ? text('管理工作台', 'Admin workspace') : text('个人工作台', 'Personal workspace') }}</p>
      <label class="wb-nav-search"><Icon name="search" size="sm" /><input v-model="search" :placeholder="text('查找页面…', 'Find a page…')" :aria-label="text('查找页面', 'Find a page')" /></label>
      <div class="wb-section-heading">{{ search ? text('搜索结果', 'Search results') : activeGroup.label }}</div>
      <nav class="wb-section-links">
        <router-link v-for="item in shownItems" :key="item.path" :to="item.path" :class="{ active: route.path === item.path }" @click="navigate">
          <component :is="item.icon" v-if="item.icon" class="h-4 w-4 shrink-0" /><Icon v-else name="document" size="sm" /><span>{{ item.label }}</span><Icon v-if="route.path === item.path" name="chevronRight" size="xs" />
        </router-link>
        <p v-if="!shownItems.length" class="wb-nav-empty">{{ text('没有匹配的页面', 'No matching pages') }}</p>
      </nav>
      <div class="wb-section-footer"><span class="wb-connection-dot" />{{ text('统一工作空间', 'Workspace') }}</div>
    </div>
  </aside>
</template>

<script setup lang="ts">
import { computed, ref, watch, onMounted, onBeforeUnmount, type Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAppStore, useAuthStore } from '@/stores'
import Icon from '@/components/icons/Icon.vue'

interface Item { path: string; label: string; icon?: unknown; children?: Item[]; expandOnly?: boolean }
const props = defineProps<{ items: Item[]; siteName: string; siteLogo: string; isDark: boolean }>()
defineEmits<{ 'toggle-theme': [] }>()
const app = useAppStore()
const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const { t, locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const search = ref('')
const flatItems = computed(() => props.items.flatMap(item => item.children?.length ? [...(item.expandOnly ? [] : [item]), ...item.children] : [item]).map(item => ({ ...item, icon: item.icon as Component | undefined })))
function category(path: string) {
  if (!path.startsWith('/admin/')) return 'personal'
  if (/\/(dashboard|ops)$/.test(path)) return 'overview'
  if (/\/(accounts|channels|groups|proxies)(\/|$)/.test(path)) return 'resources'
  if (/\/(users|subscriptions|orders|affiliates)(\/|$)/.test(path)) return 'business'
  if (/\/(usage|audit-logs|risk-control|prompt-audit)(\/|$)/.test(path)) return 'activity'
  return 'settings'
}
const definitions = computed(() => [
  { id: 'overview', label: text('总览', 'Overview'), icon: 'home' as const },
  { id: 'resources', label: text('资源', 'Resources'), icon: 'server' as const },
  { id: 'business', label: text('业务', 'Business'), icon: 'users' as const },
  { id: 'activity', label: text('记录', 'Activity'), icon: 'chart' as const },
  { id: 'settings', label: text('设置', 'Settings'), icon: 'cog' as const },
  { id: 'personal', label: text('个人', 'Personal'), icon: 'user' as const },
])
const groups = computed(() => definitions.value.map(group => ({ ...group, items: flatItems.value.filter(item => category(item.path) === group.id) })).filter(group => group.items.length))
const activeGroup = computed(() => groups.value.find(group => group.items.some(item => route.path === item.path)) || groups.value.find(group => group.id === category(route.path)) || { ...definitions.value[0], items: [] })
const shownItems = computed(() => search.value.trim() ? flatItems.value.filter(item => item.label.toLowerCase().includes(search.value.trim().toLowerCase())) : activeGroup.value.items)
function openGroup(group: typeof groups.value[number]) { search.value = ''; if (group.id !== activeGroup.value.id && group.items[0]) void router.push(group.items[0].path) }
function navigate() { search.value = ''; app.setMobileOpen(false) }
function keydown(event: KeyboardEvent) { if (event.key === 'Escape') app.setMobileOpen(false) }
watch(() => route.fullPath, navigate)
onMounted(() => window.addEventListener('keydown', keydown))
onBeforeUnmount(() => window.removeEventListener('keydown', keydown))
</script>
