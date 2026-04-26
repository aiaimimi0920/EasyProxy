<script setup>
import { computed, ref } from 'vue';
import Switch from '../../../ui/Switch.vue';
import { runAggregatorSync } from '../../../../lib/api.js';
import { useToastStore } from '../../../../stores/toast.js';
import { formatDate } from '../../../../utils/format-utils.js';

const props = defineProps({
  settings: {
    type: Object,
    required: true
  }
});

const { showToast } = useToastStore();
const isSyncing = ref(false);

const aggregatorSyncConfig = computed({
  get() {
    if (!props.settings.aggregatorSync || typeof props.settings.aggregatorSync !== 'object') {
      props.settings.aggregatorSync = {};
    }

    return {
      enabled: false,
      sourceUrl: 'https://sub.aiaimimi.com/internal/crawledsubs.json',
      managedGroup: 'Aggregator Discovery',
      namePrefix: 'Aggregator Discovery',
      secondaryProbeEnabled: true,
      stableSourceEnabled: true,
      stableSourceUrl: 'https://sub.aiaimimi.com/subs/clash.yaml',
      stableSourceName: 'Aggregator Stable',
      stableSourceGroup: 'Aggregator Stable',
      defaultPublicProfileEnabled: true,
      defaultPublicProfileCustomId: 'aggregator-global',
      defaultPublicProfileName: 'Aggregator Global',
      defaultPublicProfileConnectorIds: [],
      runOnCron: true,
      autoDisableMissing: true,
      lastSyncAt: '',
      lastSyncStatus: 'idle',
      lastSyncMessage: '',
      lastImportedCount: 0,
      lastDiscoveryImportedCount: 0,
      lastDiscoveryProbeAt: '',
      lastDiscoveryProbedCount: 0,
      lastDiscoveryVerifiedCount: 0,
      lastDiscoveryUnreachableCount: 0,
      lastDiscoveryInconclusiveCount: 0,
      lastDiscoverySkippedCount: 0,
      ...props.settings.aggregatorSync
    };
  },
  set(value) {
    props.settings.aggregatorSync = value;
  }
});

const syncStatusText = computed(() => {
  const status = aggregatorSyncConfig.value.lastSyncStatus;
  if (status === 'success') return '最近一次同步成功';
  if (status === 'error') return '最近一次同步失败';
  if (status === 'skipped') return '最近一次同步被跳过';
  return '尚未执行同步';
});

const syncStatusTone = computed(() => {
  const status = aggregatorSyncConfig.value.lastSyncStatus;
  if (status === 'success') return 'success';
  if (status === 'error') return 'danger';
  if (status === 'skipped') return 'warning';
  return 'muted';
});

const lastSyncAtText = computed(() => {
  const timestamp = aggregatorSyncConfig.value.lastSyncAt;
  if (!timestamp) return '';
  return `${formatDate(timestamp, 'relative')} (${formatDate(timestamp, 'datetime')})`;
});

const lastProbeAtText = computed(() => {
  const timestamp = aggregatorSyncConfig.value.lastDiscoveryProbeAt;
  if (!timestamp) return '';
  return `${formatDate(timestamp, 'relative')} (${formatDate(timestamp, 'datetime')})`;
});

async function handleSyncNow() {
  isSyncing.value = true;
  try {
    const response = await runAggregatorSync(props.settings);
    if (!response?.success) {
      throw new Error(response?.error || '同步失败');
    }

    if (response.data?.aggregatorSync) {
      props.settings.aggregatorSync = {
        ...aggregatorSyncConfig.value,
        ...response.data.aggregatorSync
      };
    }

    const summary = response.data?.summary;
    showToast(
      summary?.message
        ? `Aggregator 同步完成：${summary.message}`
        : 'Aggregator 同步完成',
      'success'
    );
  } catch (error) {
    showToast(`Aggregator 同步失败: ${error.message || '未知错误'}`, 'error');
  } finally {
    isSyncing.value = false;
  }
}
</script>

<template>
  <div class="bg-white/90 dark:bg-gray-900/70 misub-radius-lg p-6 space-y-5 border border-gray-100/80 dark:border-white/10 shadow-sm transition-shadow duration-300">
    <div class="flex items-start justify-between gap-4">
      <div>
        <h3 class="text-base font-semibold text-gray-900 dark:text-white flex items-center gap-2">
          <svg xmlns="http://www.w3.org/2000/svg" class="h-5 w-5 text-orange-500" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4h16v4H4zm2 6h12v10H6zm2 2v6m4-6v6m4-6v6" />
          </svg>
          Aggregator 同步
        </h3>
        <p class="text-xs text-gray-500 dark:text-gray-400 mt-1">
          自动接入 aggregator 的两层输出：`crawledsubs.json` 作为内部发现源，`clash.yaml` 作为稳定公共源，并维护默认公共 profile。
        </p>
      </div>
      <Switch v-model="aggregatorSyncConfig.enabled" />
    </div>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
      <div>
        <label class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">导出地址</label>
        <input
          v-model="aggregatorSyncConfig.sourceUrl"
          type="text"
          placeholder="https://sub.aiaimimi.com/internal/crawledsubs.json"
          class="block w-full px-3 py-2 bg-gray-50 dark:bg-gray-900/50 border border-gray-300 dark:border-gray-600 misub-radius-lg shadow-xs focus:ring-1 focus:ring-orange-500 focus:border-orange-500 sm:text-sm dark:text-white transition-colors"
        />
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">当前按 aggregator 导出的 URL -> metadata map 格式抓取。</p>
      </div>
      <div>
        <label class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">发现源分组</label>
        <input
          v-model="aggregatorSyncConfig.managedGroup"
          type="text"
          placeholder="Aggregator Discovery"
          class="block w-full px-3 py-2 bg-gray-50 dark:bg-gray-900/50 border border-gray-300 dark:border-gray-600 misub-radius-lg shadow-xs focus:ring-1 focus:ring-orange-500 focus:border-orange-500 sm:text-sm dark:text-white transition-colors"
        />
      </div>
    </div>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
      <div>
        <label class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">名称前缀</label>
        <input
          v-model="aggregatorSyncConfig.namePrefix"
          type="text"
          placeholder="Aggregator Discovery"
          class="block w-full px-3 py-2 bg-gray-50 dark:bg-gray-900/50 border border-gray-300 dark:border-gray-600 misub-radius-lg shadow-xs focus:ring-1 focus:ring-orange-500 focus:border-orange-500 sm:text-sm dark:text-white transition-colors"
        />
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">内部发现源会用这个前缀生成默认名称。</p>
      </div>

      <div class="space-y-3">
        <div class="flex items-center justify-between p-4 bg-white/70 dark:bg-gray-900/50 border border-gray-200/70 dark:border-white/10 misub-radius-lg">
          <div>
            <p class="text-sm font-medium text-gray-900 dark:text-gray-200">维护稳定源</p>
            <p class="text-xs text-gray-500 dark:text-gray-400 mt-0.5">自动维护一个默认稳定源，指向 aggregator 最终成品 `clash.yaml`。</p>
          </div>
          <Switch v-model="aggregatorSyncConfig.stableSourceEnabled" />
        </div>
        <div class="flex items-center justify-between p-4 bg-white/70 dark:bg-gray-900/50 border border-gray-200/70 dark:border-white/10 misub-radius-lg">
          <div>
            <p class="text-sm font-medium text-gray-900 dark:text-gray-200">发现源二次探测</p>
            <p class="text-xs text-gray-500 dark:text-gray-400 mt-0.5">同步完成后，再对 crawler 导入的发现源做一次 MiSub 本地探测，但默认公共 profile 仍只输出稳定源。</p>
          </div>
          <Switch v-model="aggregatorSyncConfig.secondaryProbeEnabled" />
        </div>
        <div class="flex items-center justify-between p-4 bg-white/70 dark:bg-gray-900/50 border border-gray-200/70 dark:border-white/10 misub-radius-lg">
          <div>
            <p class="text-sm font-medium text-gray-900 dark:text-gray-200">维护默认公共 Profile</p>
            <p class="text-xs text-gray-500 dark:text-gray-400 mt-0.5">默认公共 profile 会挂稳定源，并可额外挂上显式指定的 connector；不会直接挂 crawler 原始发现源。</p>
          </div>
          <Switch v-model="aggregatorSyncConfig.defaultPublicProfileEnabled" />
        </div>
        <div class="flex items-center justify-between p-4 bg-white/70 dark:bg-gray-900/50 border border-gray-200/70 dark:border-white/10 misub-radius-lg">
          <div>
            <p class="text-sm font-medium text-gray-900 dark:text-gray-200">Cron 自动同步</p>
            <p class="text-xs text-gray-500 dark:text-gray-400 mt-0.5">`/cron` 执行时先同步 aggregator 导出的订阅列表，再刷新订阅统计。</p>
          </div>
          <Switch v-model="aggregatorSyncConfig.runOnCron" />
        </div>
        <div class="flex items-center justify-between p-4 bg-white/70 dark:bg-gray-900/50 border border-gray-200/70 dark:border-white/10 misub-radius-lg">
          <div>
            <p class="text-sm font-medium text-gray-900 dark:text-gray-200">自动禁用缺失项</p>
            <p class="text-xs text-gray-500 dark:text-gray-400 mt-0.5">当某个托管源不再出现在最新 export 中时，将其自动标记为 disabled。</p>
          </div>
          <Switch v-model="aggregatorSyncConfig.autoDisableMissing" />
        </div>
      </div>
    </div>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
      <div>
        <label class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">稳定源地址</label>
        <input
          v-model="aggregatorSyncConfig.stableSourceUrl"
          type="text"
          placeholder="https://sub.aiaimimi.com/subs/clash.yaml"
          class="block w-full px-3 py-2 bg-gray-50 dark:bg-gray-900/50 border border-gray-300 dark:border-gray-600 misub-radius-lg shadow-xs focus:ring-1 focus:ring-orange-500 focus:border-orange-500 sm:text-sm dark:text-white transition-colors"
        />
      </div>
      <div>
        <label class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">稳定源名称</label>
        <input
          v-model="aggregatorSyncConfig.stableSourceName"
          type="text"
          placeholder="Aggregator Stable"
          class="block w-full px-3 py-2 bg-gray-50 dark:bg-gray-900/50 border border-gray-300 dark:border-gray-600 misub-radius-lg shadow-xs focus:ring-1 focus:ring-orange-500 focus:border-orange-500 sm:text-sm dark:text-white transition-colors"
        />
      </div>
    </div>

    <div v-if="aggregatorSyncConfig.defaultPublicProfileEnabled" class="grid grid-cols-1 md:grid-cols-2 gap-6">
      <div>
        <label class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">默认公共 Profile 名称</label>
        <input
          v-model="aggregatorSyncConfig.defaultPublicProfileName"
          type="text"
          placeholder="Aggregator Global"
          class="block w-full px-3 py-2 bg-gray-50 dark:bg-gray-900/50 border border-gray-300 dark:border-gray-600 misub-radius-lg shadow-xs focus:ring-1 focus:ring-orange-500 focus:border-orange-500 sm:text-sm dark:text-white transition-colors"
        />
      </div>
      <div>
        <label class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">默认公共 Profile Custom ID</label>
        <input
          v-model="aggregatorSyncConfig.defaultPublicProfileCustomId"
          type="text"
          placeholder="aggregator-global"
          class="block w-full px-3 py-2 bg-gray-50 dark:bg-gray-900/50 border border-gray-300 dark:border-gray-600 misub-radius-lg shadow-xs focus:ring-1 focus:ring-orange-500 focus:border-orange-500 sm:text-sm dark:text-white transition-colors"
        />
      </div>
    </div>

    <div v-if="aggregatorSyncConfig.defaultPublicProfileEnabled">
      <label class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">默认公共 Profile Connector IDs</label>
      <textarea
        :value="(aggregatorSyncConfig.defaultPublicProfileConnectorIds || []).join('\n')"
        @input="aggregatorSyncConfig.defaultPublicProfileConnectorIds = $event.target.value.split(/[\r\n,]+/).map(item => item.trim()).filter(Boolean)"
        rows="4"
        placeholder="每行一个 connector source id，例如 conn_ech_workers_pref_1"
        class="block w-full px-3 py-2 bg-gray-50 dark:bg-gray-900/50 border border-gray-300 dark:border-gray-600 misub-radius-lg shadow-xs focus:ring-1 focus:ring-orange-500 focus:border-orange-500 sm:text-sm dark:text-white transition-colors font-mono"
      />
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
        这里填写的 connector 会直接作为 MiSub 的 profile manualNodes 输出到 machine manifest。
        它们不是 aggregator 发现源，而是 MiSub 自己托管的手动来源 / ECH Worker metadata。
      </p>
    </div>

    <div class="misub-radius-lg border px-4 py-3 text-sm"
      :class="{
        'bg-emerald-50/80 text-emerald-700 border-emerald-200 dark:bg-emerald-500/10 dark:text-emerald-200 dark:border-emerald-500/20': syncStatusTone === 'success',
        'bg-red-50/80 text-red-700 border-red-200 dark:bg-red-500/10 dark:text-red-200 dark:border-red-500/20': syncStatusTone === 'danger',
        'bg-amber-50/80 text-amber-700 border-amber-200 dark:bg-amber-500/10 dark:text-amber-200 dark:border-amber-500/20': syncStatusTone === 'warning',
        'bg-gray-50/80 text-gray-600 border-gray-200 dark:bg-white/5 dark:text-gray-300 dark:border-white/10': syncStatusTone === 'muted'
      }">
      <div class="font-semibold mb-1">{{ syncStatusText }}</div>
      <div>{{ aggregatorSyncConfig.lastSyncMessage || '保存设置后，可通过按钮立即触发同步。' }}</div>
      <div v-if="lastSyncAtText || aggregatorSyncConfig.lastImportedCount" class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs opacity-80">
        <span v-if="lastSyncAtText">最近同步：{{ lastSyncAtText }}</span>
        <span>当前托管源：{{ aggregatorSyncConfig.lastImportedCount || 0 }}</span>
        <span>当前发现源：{{ aggregatorSyncConfig.lastDiscoveryImportedCount || 0 }}</span>
        <span v-if="lastProbeAtText">最近二次探测：{{ lastProbeAtText }}</span>
        <span>探测总数：{{ aggregatorSyncConfig.lastDiscoveryProbedCount || 0 }}</span>
        <span>探测通过：{{ aggregatorSyncConfig.lastDiscoveryVerifiedCount || 0 }}</span>
        <span>探测不可达：{{ aggregatorSyncConfig.lastDiscoveryUnreachableCount || 0 }}</span>
        <span>探测未定：{{ aggregatorSyncConfig.lastDiscoveryInconclusiveCount || 0 }}</span>
        <span>探测跳过：{{ aggregatorSyncConfig.lastDiscoverySkippedCount || 0 }}</span>
      </div>
    </div>

    <div class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 dark:border-gray-700 pt-4">
      <p class="text-xs text-gray-500 dark:text-gray-400">
        “立即同步”会使用当前表单里的配置执行一次同步，并把同步状态写回设置。后续正式运行仍以保存后的设置为准。
      </p>
      <button
        @click="handleSyncNow"
        :disabled="isSyncing || !aggregatorSyncConfig.enabled || !aggregatorSyncConfig.sourceUrl"
        class="px-4 py-2 bg-orange-600 hover:bg-orange-700 text-white text-sm font-medium misub-radius-lg disabled:opacity-50 disabled:cursor-not-allowed transition-colors flex items-center gap-2"
      >
        <svg v-if="isSyncing" class="animate-spin h-4 w-4 text-white" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
          <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
          <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
        </svg>
        <span>{{ isSyncing ? '同步中...' : '立即同步' }}</span>
      </button>
    </div>
  </div>
</template>
