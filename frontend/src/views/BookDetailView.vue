<script setup lang="ts">
import { nextTick, onMounted, ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { deleteBook, getBook, type ApiError, type Book } from '@/api'
import { useNoticeStore } from '@/stores/notice'

const props = defineProps<{ id: number }>()

const router = useRouter()
const notice = useNoticeStore()

const loading = ref(true)
const loadError = ref<ApiError | null>(null)
const book = ref<Book | null>(null)

const confirming = ref(false)
const deleting = ref(false)
const deleteError = ref<string | null>(null)
const cancelButton = ref<HTMLButtonElement | null>(null)

const NONE = '—'

function formatDate(value: string): string {
  return new Date(value).toLocaleString()
}

async function load() {
  loading.value = true
  loadError.value = null
  const result = await getBook(props.id)
  if (result.ok) book.value = result.data
  else loadError.value = result.error
  loading.value = false
}

async function openConfirm() {
  deleteError.value = null
  confirming.value = true
  await nextTick()
  cancelButton.value?.focus()
}

function cancelConfirm() {
  if (deleting.value) return
  confirming.value = false
  deleteError.value = null
}

async function confirmDelete() {
  if (deleting.value || book.value === null) return
  deleting.value = true
  deleteError.value = null
  const title = book.value.title
  const result = await deleteBook(props.id)
  if (result.ok || result.error.kind === 'not-found') {
    await router.push({ name: 'books' })
    notice.show('Deleted ' + title)
    return
  }
  deleteError.value = result.error.message
  deleting.value = false
}

onMounted(load)
</script>

<template>
  <p v-if="loading" role="status">Loading book…</p>

  <section v-else-if="loadError !== null && loadError.kind === 'not-found'">
    <h1>Book not found</h1>
    <RouterLink to="/books">Back to the catalog</RouterLink>
  </section>

  <div v-else-if="loadError !== null" role="alert">
    <p>Could not load the book: {{ loadError.message }}</p>
    <button type="button" @click="load">Retry</button>
  </div>

  <article v-else-if="book !== null">
    <h1>{{ book.title }}</h1>
    <dl>
      <dt>ISBN</dt>
      <dd>{{ book.isbn }}</dd>
      <dt>Title</dt>
      <dd>{{ book.title }}</dd>
      <dt>Authors</dt>
      <dd>
        <ul>
          <li v-for="(author, index) in book.authors" :key="index">{{ author }}</li>
        </ul>
      </dd>
      <dt>Published year</dt>
      <dd>{{ book.publishedYear ?? NONE }}</dd>
      <dt>Language</dt>
      <dd>{{ book.language ?? NONE }}</dd>
      <dt>Pages</dt>
      <dd>{{ book.pages ?? NONE }}</dd>
      <dt>Created</dt>
      <dd>{{ formatDate(book.createdAt) }}</dd>
      <dt>Updated</dt>
      <dd>{{ formatDate(book.updatedAt) }}</dd>
      <dt>Version</dt>
      <dd>{{ book.version }}</dd>
    </dl>

    <div class="actions">
      <RouterLink :to="{ name: 'book-edit', params: { id: props.id } }">Edit</RouterLink>
      <RouterLink to="/books">Back to the catalog</RouterLink>
      <button v-if="!confirming" type="button" @click="openConfirm">Delete</button>
    </div>

    <div v-if="confirming" role="group" aria-labelledby="delete-question" class="confirm">
      <p id="delete-question">Delete "{{ book.title }}"? This cannot be undone.</p>
      <p v-if="deleteError !== null" role="alert">{{ deleteError }}</p>
      <button ref="cancelButton" type="button" :disabled="deleting" @click="cancelConfirm">
        Cancel
      </button>
      <button type="button" :disabled="deleting" @click="confirmDelete">Delete</button>
    </div>
  </article>
</template>

<style scoped>
.actions {
  display: flex;
  gap: 1rem;
  align-items: center;
}
.confirm {
  margin-top: 1rem;
  padding: 0.75rem;
  border: 1px solid #c00;
}
dt {
  font-weight: bold;
}
</style>
