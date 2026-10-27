<script setup lang="ts">
const MAX_AUTHORS = 20

defineProps<{ error?: string }>()

const authors = defineModel<string[]>({ required: true })

function setAuthor(index: number, event: Event) {
  if (!(event.target instanceof HTMLInputElement)) return
  const next = [...authors.value]
  next[index] = event.target.value
  authors.value = next
}

function addAuthor() {
  if (authors.value.length >= MAX_AUTHORS) return
  authors.value = [...authors.value, '']
}

function removeAuthor(index: number) {
  if (authors.value.length <= 1) return
  authors.value = authors.value.filter((_, i) => i !== index)
}
</script>

<template>
  <fieldset
    aria-required="true"
    :aria-invalid="error !== undefined ? 'true' : undefined"
    :aria-describedby="error !== undefined ? 'book-authors-error' : undefined"
  >
    <legend>Authors<span class="required" aria-hidden="true"> *</span></legend>
    <div v-for="(author, index) in authors" :key="index" class="author">
      <label :for="`book-author-${index + 1}`">Author {{ index + 1 }}</label>
      <input
        :id="`book-author-${index + 1}`"
        type="text"
        :value="author"
        aria-required="true"
        :aria-invalid="error !== undefined ? 'true' : undefined"
        :aria-describedby="error !== undefined ? 'book-authors-error' : undefined"
        @input="setAuthor(index, $event)"
      />
      <button
        type="button"
        :aria-label="`Remove author ${index + 1}`"
        :disabled="authors.length <= 1"
        @click="removeAuthor(index)"
      >
        Remove
      </button>
    </div>
    <button type="button" :disabled="authors.length >= MAX_AUTHORS" @click="addAuthor">
      Add author
    </button>
    <p v-if="error !== undefined" id="book-authors-error" class="field-error">{{ error }}</p>
  </fieldset>
</template>

<style scoped>
fieldset {
  margin: 0 0 0.75rem;
}

.author {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 0.5rem;
}

.required,
.field-error {
  color: #b00020;
}

.field-error {
  margin: 0.5rem 0 0;
}
</style>
