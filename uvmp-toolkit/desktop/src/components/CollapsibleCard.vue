<template>
  <t-card class="collapsible-card">
    <button class="collapsible-head" @click="$emit('update:open', !open)">
      <span>{{ title }}</span>
      <span class="chevron">{{ open ? '−' : '+' }}</span>
    </button>
    <div v-show="open" class="collapsible-body">
      <slot />
    </div>
  </t-card>
</template>

<script setup>
defineProps({
  title: { type: String, required: true },
  open: { type: Boolean, default: false },
});
defineEmits(['update:open']);
</script>
