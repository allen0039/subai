<template>
  <div class="wb-shell" data-testid="workbench-shell">
    <AppSidebar v-slot="{ items, siteName, siteLogo, toggleTheme, isDark }">
      <WorkbenchNavigation :items="items" :site-name="siteName" :site-logo="siteLogo" :is-dark="isDark" @toggle-theme="toggleTheme" />
    </AppSidebar>
    <div class="wb-body">
      <AppHeader />
      <main id="workbench-content" class="wb-content"><slot /></main>
    </div>
  </div>
</template>
<script setup lang="ts">
import AppHeader from './AppHeader.vue'
import AppSidebar from './AppSidebar.vue'
import WorkbenchNavigation from './WorkbenchNavigation.vue'
import '@/styles/workbench.css'
</script>
