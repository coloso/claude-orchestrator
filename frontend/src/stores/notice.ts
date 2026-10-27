import { ref } from 'vue'
import { defineStore } from 'pinia'

export const useNoticeStore = defineStore('notice', () => {
  const message = ref<string | null>(null)

  function show(text: string): void {
    message.value = text
  }

  function clear(): void {
    message.value = null
  }

  return { message, show, clear }
})
