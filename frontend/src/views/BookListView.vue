<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter, type LocationQueryRaw, type LocationQueryValue } from 'vue-router'
import { listBooks, PAGE_SIZE, type ApiError, type BookList } from '@/api'

const route = useRoute()
const router = useRouter()

function first(value: LocationQueryValue | LocationQueryValue[] | undefined): string {
  const one = Array.isArray(value) ? value[0] : value
  return one ?? ''
}

function parseOffset(raw: string): number {
  if (!/^\d+$/.test(raw)) return 0
  const n = Number(raw)
  return Number.isSafeInteger(n) ? n : 0
}

function listQuery(author: string, offset: number): LocationQueryRaw {
  const query: LocationQueryRaw = {}
  if (author !== '') query.author = author
  if (offset > 0) query.offset = String(offset)
  return query
}

const author = computed(() => first(route.query.author).trim())
const offset = computed(() => parseOffset(first(route.query.offset)))

const authorInput = ref(author.value)
const loading = ref(false)
const error = ref<ApiError | null>(null)
const list = ref<BookList | null>(null)
let latest = 0

async function load(): Promise<void> {
  const request = ++latest
  loading.value = true
  error.value = null
  const query = { offset: offset.value, limit: PAGE_SIZE, ...(author.value !== '' ? { author: author.value } : {}) }
  const result = await listBooks(query)
  if (request !== latest) return
  loading.value = false
  if (result.ok) list.value = result.data
  else error.value = result.error
}

watch(
  () => route.query,
  () => {
    if (route.name !== 'books') return
    authorInput.value = author.value
    void load()
  },
  { immediate: true },
)

const items = computed(() => list.value?.items ?? [])
const total = computed(() => list.value?.total ?? 0)
const summary = computed(() => `${offset.value + 1}–${offset.value + items.value.length} of ${total.value}`)
const hasPrevious = computed(() => offset.value > 0)
const hasNext = computed(() => offset.value + items.value.length < total.value)
const firstPage = computed(() => ({ name: 'books', query: listQuery(author.value, 0) }))

function go(newOffset: number): void {
  if (loading.value) return
  void router.push({ name: 'books', query: listQuery(author.value, Math.max(0, newOffset)) })
}

function applyFilter(): void {
  if (loading.value) return
  void router.push({ name: 'books', query: listQuery(authorInput.value.trim(), 0) })
}

function clearFilter(): void {
  void router.push({ name: 'books' })
}
</script>

<template>
  <h1>Catalog</h1>

  <div class="toolbar">
    <form class="filter" @submit.prevent="applyFilter">
      <label for="author-filter">Author</label>
      <input id="author-filter" v-model="authorInput" type="text" />
      <button type="submit" :disabled="loading">Filter</button>
      <button v-if="author !== ''" type="button" @click="clearFilter">Clear filter</button>
    </form>
    <RouterLink :to="{ name: 'book-new' }">New book</RouterLink>
  </div>

  <p v-if="loading" role="status">Loading books…</p>

  <div v-if="error !== null" role="alert">
    <p>Could not load the books: {{ error.message }}</p>
    <button type="button" @click="load()">Retry</button>
  </div>

  <template v-else-if="list !== null">
    <p v-if="items.length === 0 && total === 0 && author === ''">No books yet</p>
    <div v-else-if="items.length === 0 && total === 0">
      <p>No books by {{ author }}</p>
      <button type="button" @click="clearFilter">Clear filter</button>
    </div>
    <div v-else-if="items.length === 0">
      <p>No books on this page</p>
      <RouterLink :to="firstPage">Go to the first page</RouterLink>
    </div>

    <template v-else>
      <table>
        <thead>
          <tr>
            <th>Title</th>
            <th>Authors</th>
            <th>Published</th>
            <th>Language</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="book in items" :key="book.id">
            <td>
              <RouterLink :to="{ name: 'book', params: { id: book.id } }">{{ book.title }}</RouterLink>
            </td>
            <td>{{ book.authors.join(', ') }}</td>
            <td>{{ book.publishedYear ?? '—' }}</td>
            <td>{{ book.language ?? '—' }}</td>
          </tr>
        </tbody>
      </table>

      <div class="pager">
        <button type="button" :disabled="loading || !hasPrevious" @click="go(offset - PAGE_SIZE)">Previous</button>
        <span>{{ summary }}</span>
        <button type="button" :disabled="loading || !hasNext" @click="go(offset + PAGE_SIZE)">Next</button>
      </div>
    </template>
  </template>
</template>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 1rem;
}

.filter {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

table {
  border-collapse: collapse;
  width: 100%;
}

th,
td {
  text-align: left;
  padding: 0.25rem 0.5rem;
  border-bottom: 1px solid #ddd;
}

.pager {
  display: flex;
  align-items: center;
  gap: 1rem;
  margin-top: 1rem;
}
</style>
