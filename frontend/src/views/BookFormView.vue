<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import {
  createBook,
  getBook,
  parseFieldError,
  updateBook,
  type ApiError,
  type Book,
  type BookField,
  type BookInput,
} from '@/api'
import AuthorsFieldset from '@/components/book-form/AuthorsFieldset.vue'
import FormField from '@/components/book-form/FormField.vue'

const props = defineProps<{ id?: number }>()

const router = useRouter()

type LoadState = 'loading' | 'ready' | 'not-found' | 'error'
// What is shown at the top of the form, besides field errors.
type TopError = { kind: 'message'; text: string } | { kind: 'conflict' } | { kind: 'gone' }

const DUPLICATE_ISBN = 'A book with this ISBN already exists'

const loadState = ref<LoadState>(props.id === undefined ? 'ready' : 'loading')
const loadError = ref('')
const loadingBook = ref(false)

const form = reactive({
  isbn: '',
  title: '',
  authors: [''],
  publishedYear: '' as string | number,
  language: '',
  pages: '' as string | number,
})
// The version the form was loaded with; sent unchanged on every revision.
const version = ref(0)

const fieldErrors = ref<Partial<Record<BookField, string>>>({})
const topError = ref<TopError | null>(null)
const reloadError = ref<string | null>(null)
const saving = ref(false)
const reloading = ref(false)

const isEdit = computed(() => props.id !== undefined)
const cancelTo = computed(() => (props.id === undefined ? '/books' : `/books/${props.id}`))

function fill(book: Book) {
  form.isbn = book.isbn
  form.title = book.title
  form.authors = book.authors.length > 0 ? [...book.authors] : ['']
  form.publishedYear = book.publishedYear ?? ''
  form.language = book.language ?? ''
  form.pages = book.pages ?? ''
  version.value = book.version
}

function clearErrors() {
  fieldErrors.value = {}
  topError.value = null
  reloadError.value = null
}

async function load() {
  if (props.id === undefined || loadingBook.value) return
  loadingBook.value = true
  loadState.value = 'loading'
  try {
    const result = await getBook(props.id)
    if (result.ok) {
      fill(result.data)
      loadState.value = 'ready'
    } else if (result.error.kind === 'not-found') {
      loadState.value = 'not-found'
    } else {
      loadError.value = result.error.message
      loadState.value = 'error'
    }
  } finally {
    loadingBook.value = false
  }
}

function isEmpty(value: string | number): boolean {
  return typeof value === 'string' && value.trim() === ''
}

function buildBody(): BookInput {
  const body: BookInput = { isbn: form.isbn, title: form.title, authors: [...form.authors] }
  if (!isEmpty(form.publishedYear)) body.publishedYear = Number(form.publishedYear)
  if (!isEmpty(form.language)) body.language = form.language
  if (!isEmpty(form.pages)) body.pages = Number(form.pages)
  return body
}

function showSaveError(error: ApiError) {
  switch (error.kind) {
    case 'invalid': {
      const parsed = parseFieldError(error.message)
      if (parsed !== null) fieldErrors.value = { [parsed.field]: parsed.reason }
      else topError.value = { kind: 'message', text: error.message }
      return
    }
    case 'duplicate-isbn':
      fieldErrors.value = { isbn: DUPLICATE_ISBN }
      return
    case 'version-conflict':
      topError.value = { kind: 'conflict' }
      return
    case 'not-found':
      topError.value = isEdit.value ? { kind: 'gone' } : { kind: 'message', text: error.message }
      return
    default:
      topError.value = { kind: 'message', text: error.message }
  }
}

async function submit() {
  if (saving.value || reloading.value) return
  saving.value = true
  clearErrors()
  try {
    const body = buildBody()
    const result =
      props.id === undefined
        ? await createBook(body)
        : await updateBook(props.id, { ...body, version: version.value })
    if (result.ok) {
      await router.push({ name: 'book', params: { id: result.data.id } })
    } else {
      showSaveError(result.error)
    }
  } finally {
    saving.value = false
  }
}

async function loadLatest() {
  if (props.id === undefined || reloading.value || saving.value) return
  reloading.value = true
  try {
    const result = await getBook(props.id)
    if (result.ok) {
      fill(result.data)
      clearErrors()
    } else if (result.error.kind === 'not-found') {
      clearErrors()
      topError.value = { kind: 'gone' }
    } else {
      reloadError.value = result.error.message
    }
  } finally {
    reloading.value = false
  }
}

onMounted(load)
</script>

<template>
  <section v-if="loadState === 'not-found'">
    <h1>Book not found</h1>
    <RouterLink to="/books">Back to books</RouterLink>
  </section>

  <section v-else>
    <h1>{{ isEdit ? 'Edit book' : 'New book' }}</h1>

    <p v-if="loadState === 'loading'" role="status">Loading book…</p>

    <div v-else-if="loadState === 'error'" class="top-error" role="alert">
      <p>{{ loadError }}</p>
      <button type="button" :disabled="loadingBook" @click="load">Retry</button>
    </div>

    <form v-else novalidate @submit.prevent="submit">
      <p v-if="topError?.kind === 'message'" class="top-error" role="alert">
        {{ topError.text }}
      </p>
      <div v-else-if="topError?.kind === 'conflict'" class="top-error" role="alert">
        <p>This book was changed by someone else.</p>
        <p>
          Load latest replaces the form with the current book and discards your changes. Until
          then, your input is kept.
        </p>
        <button type="button" :disabled="reloading" @click="loadLatest">Load latest</button>
      </div>
      <p v-else-if="topError?.kind === 'gone'" class="top-error" role="alert">
        This book no longer exists.
        <RouterLink to="/books">Back to books</RouterLink>
      </p>
      <p v-if="reloadError !== null" class="top-error" role="alert">{{ reloadError }}</p>

      <FormField
        id="book-isbn"
        v-model="form.isbn"
        label="ISBN"
        required
        :error="fieldErrors.isbn"
      />
      <FormField
        id="book-title"
        v-model="form.title"
        label="Title"
        required
        :error="fieldErrors.title"
      />
      <AuthorsFieldset v-model="form.authors" :error="fieldErrors.authors" />
      <FormField
        id="book-publishedYear"
        v-model="form.publishedYear"
        label="Published year"
        type="number"
        :error="fieldErrors.publishedYear"
      />
      <FormField
        id="book-language"
        v-model="form.language"
        label="Language"
        :error="fieldErrors.language"
      />
      <FormField
        id="book-pages"
        v-model="form.pages"
        label="Pages"
        type="number"
        :error="fieldErrors.pages"
      />

      <div class="actions">
        <button type="submit" :disabled="saving || reloading">
          {{ isEdit ? 'Save' : 'Add book' }}
        </button>
        <RouterLink :to="cancelTo">Cancel</RouterLink>
      </div>
    </form>
  </section>
</template>

<style scoped>
.top-error {
  padding: 0.5rem 0.75rem;
  margin: 0 0 1rem;
  border: 1px solid #b00020;
  color: #b00020;
}

.top-error p {
  margin: 0 0 0.5rem;
}

.actions {
  display: flex;
  align-items: center;
  gap: 1rem;
}
</style>
