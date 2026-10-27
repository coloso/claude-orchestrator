<script setup lang="ts">
import { onUnmounted } from 'vue'
import { RouterLink, RouterView, useRouter } from 'vue-router'
import { useNoticeStore } from '@/stores/notice'

const notice = useNoticeStore()
const router = useRouter()

// A notice belongs to the page it was shown on; views call show() after their push has resolved.
const removeHook = router.afterEach(() => notice.clear())
onUnmounted(removeHook)
</script>

<template>
  <header class="app-header">
    <strong>Book catalog</strong>
    <nav>
      <RouterLink to="/books">Books</RouterLink>
    </nav>
  </header>
  <div v-if="notice.message !== null" class="notice" role="status">
    <span>{{ notice.message }}</span>
    <button type="button" @click="notice.clear()">Dismiss</button>
  </div>
  <main>
    <RouterView v-slot="{ Component, route }">
      <component :is="Component" :key="route.path" />
    </RouterView>
  </main>
</template>

<style scoped>
.app-header {
  display: flex;
  align-items: center;
  gap: 1.5rem;
  padding: 0.75rem 1rem;
  border-bottom: 1px solid #ccc;
}

.notice {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.5rem 1rem;
  background: #e8f4e8;
}

main {
  padding: 1rem;
}
</style>
