<script setup lang="ts">
import { DialogClose, DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogRoot, DialogTitle, DialogTrigger } from 'reka-ui'
import { X } from 'lucide-vue-next'
defineProps<{ open: boolean; title: string; description?: string; wide?: boolean; size?: 'default' | 'wide' | 'xwide' }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
</script>
<template>
  <DialogRoot :open="open" @update:open="emit('update:open', $event)">
    <DialogTrigger v-if="$slots.trigger" as-child><slot name="trigger" /></DialogTrigger>
    <DialogPortal>
      <DialogOverlay class="dialog-overlay" />
      <DialogContent class="dialog-content" :class="{ wide: size === 'wide' || (!size && wide), xwide: size === 'xwide' }">
        <DialogTitle class="dialog-title">{{ title }}</DialogTitle>
        <DialogDescription v-if="description" class="sr-only">{{ description }}</DialogDescription>
        <DialogClose class="icon-button dialog-close" aria-label="关闭"><X :size="20" /></DialogClose>
        <slot />
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
