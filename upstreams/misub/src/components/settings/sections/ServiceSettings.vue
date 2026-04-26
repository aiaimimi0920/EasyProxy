<script setup>
import SubConverterCard from './ServiceSettings/SubConverterCard.vue';
import AggregatorSyncCard from './ServiceSettings/AggregatorSyncCard.vue';
import TelegramCard from './ServiceSettings/TelegramCard.vue';
import CronCard from './ServiceSettings/CronCard.vue';

defineProps({
  settings: {
    type: Object,
    required: true
  }
});
</script>

<template>
  <div class="space-y-8">
    <SubConverterCard :settings="settings" />
    <AggregatorSyncCard :settings="settings" />
    <TelegramCard :settings="settings" />
    <CronCard :settings="settings" />
  </div>
</template>
