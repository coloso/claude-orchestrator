<script setup lang="ts">
defineProps<{
  id: string
  label: string
  type?: 'text' | 'number'
  required?: boolean
  error?: string
}>()

// A number input yields a number, or '' when empty.
const model = defineModel<string | number>({ required: true })
</script>

<template>
  <div class="field">
    <label :for="id">
      {{ label }}
      <span v-if="required" class="required" aria-hidden="true">*</span>
    </label>
    <input
      :id="id"
      v-model="model"
      :type="type ?? 'text'"
      :aria-required="required ? 'true' : undefined"
      :aria-invalid="error !== undefined ? 'true' : undefined"
      :aria-describedby="error !== undefined ? `${id}-error` : undefined"
    />
    <p v-if="error !== undefined" :id="`${id}-error`" class="field-error">{{ error }}</p>
  </div>
</template>

<style scoped>
.field {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  margin-bottom: 0.75rem;
}

.required {
  color: #b00020;
}

.field-error {
  margin: 0;
  color: #b00020;
}
</style>
