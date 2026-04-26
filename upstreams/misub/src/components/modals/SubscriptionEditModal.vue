<script setup>
import { computed, ref, watch } from 'vue';
import { useToastStore } from '../../stores/toast.js';
import Modal from '../forms/Modal.vue';
import EditForm from './SubscriptionEditModal/EditForm.vue';
import RuleSection from './SubscriptionEditModal/RuleSection.vue';
import AdvancedOptions from './SubscriptionEditModal/AdvancedOptions.vue';
import { probeSource as probeSourceRequest } from '../../lib/api.js';
import { formatDate } from '../../utils/format-utils.js';
import { canManuallyProbeSource, getSourceProbeSummary, normalizeSourceItem } from '../../shared/source-utils.js';
const props = defineProps({
  show: Boolean,
  isNew: Boolean,
  editingSubscription: Object
});

const emit = defineEmits(['update:show', 'confirm']);
const { showToast } = useToastStore();


// === 可视化规则编辑器 ===
const isAdvancedMode = ref(false);
const isRuleExpanded = ref(false); // 折叠状态
const ruleMode = ref('exclude'); // 'exclude' | 'keep'
const customKeyword = ref('');

// 预设标签
const presetRegions = [
  { label: '香港', pattern: '(香港|HK|Hong Kong)', icon: '🇭🇰' },
  { label: '台湾', pattern: '(台湾|TW|Taiwan)', icon: '🇨🇳' },
  { label: '日本', pattern: '(日本|JP|Japan)', icon: '🇯🇵' },
  { label: '新加坡', pattern: '(新加坡|SG|Singapore)', icon: '🇸🇬' },
  { label: '美国', pattern: '(美国|US|USA)', icon: '🇺🇸' },
  { label: '韩国', pattern: '(韩国|KR|Korea)', icon: '🇰🇷' },
  { label: '德国', pattern: '(德国|DE|Germany)', icon: '🇩🇪' },
  { label: '英国', pattern: '(英国|UK|Britain)', icon: '🇬🇧' },
];

const presetProtocols = [
  { label: 'SS', pattern: 'proto:ss' },
  { label: 'VMess', pattern: 'proto:vmess' },
  { label: 'VLESS', pattern: 'proto:vless' },
  { label: 'Trojan', pattern: 'proto:trojan' },
  { label: 'Hysteria', pattern: 'proto:hysteria' },
];

const presetKeywords = [
  { label: '官网', pattern: '官网' },
  { label: '过期', pattern: '过期' },
  { label: '剩余', pattern: '(剩余|流量)' },
  { label: '倍率', pattern: '倍率' },
  { label: '测试', pattern: '测试' },
  { label: '维护', pattern: '维护' },
  { label: 'GB', pattern: 'GB' },
  { label: '2026', pattern: '2026' },
];

// 已选择的规则（内置+自定义）
const selectedRules = ref([]);

// 检查标签是否已选中
const isSelected = (pattern) => {
  return selectedRules.value.some(rule => rule.pattern === pattern);
};

// 切换标签选择
const toggleTag = (tag, type) => {
  const index = selectedRules.value.findIndex(rule => rule.pattern === tag.pattern);
  if (index !== -1) {
    selectedRules.value.splice(index, 1);
  } else {
    selectedRules.value.push({
      ...tag,
      type // 'region' | 'protocol' | 'keyword' | 'custom'
    });
  }
  syncToText();
};

// 添加自定义关键字
const addCustomKeyword = () => {
  const keyword = customKeyword.value.trim();
  if (!keyword) return;

  // 检查是否已存在
  if (selectedRules.value.some(rule => rule.pattern === keyword || rule.label === keyword)) {
    showToast('该关键字已添加', 'warning');
    return;
  }

  selectedRules.value.push({
    label: keyword,
    pattern: keyword,
    type: 'custom'
  });
  customKeyword.value = '';
  syncToText();
};

// 移除规则
const removeRule = (index) => {
  selectedRules.value.splice(index, 1);
  syncToText();
};

// 将可视化选择同步到文本格式
const syncToText = () => {
  if (!props.editingSubscription) return;

  const rules = selectedRules.value.map(rule => {
    if (ruleMode.value === 'keep') {
      return rule.pattern.startsWith('proto:')
        ? `keep:${rule.pattern}`
        : `keep:${rule.pattern}`;
    }
    return rule.pattern;
  });

  props.editingSubscription.exclude = rules.join('\n');
};

// 从文本解析到可视化状态
const parseFromText = () => {
  if (!props.editingSubscription?.exclude) {
    selectedRules.value = [];
    ruleMode.value = 'exclude';
    return;
  }

  const text = props.editingSubscription.exclude;
  const lines = text.split('\n').map(l => l.trim()).filter(Boolean);

  // 检测模式
  const hasKeep = lines.some(l => l.toLowerCase().startsWith('keep:'));
  ruleMode.value = hasKeep ? 'keep' : 'exclude';

  // 如果包含 --- 分隔符或复杂混合规则，使用高级模式
  if (lines.includes('---')) {
    isAdvancedMode.value = true;
    return;
  }

  selectedRules.value = [];

  lines.forEach(line => {
    let pattern = line;
    if (pattern.toLowerCase().startsWith('keep:')) {
      pattern = pattern.substring(5).trim();
    }

    // 尝试匹配预设标签
    const allPresets = [...presetRegions, ...presetProtocols, ...presetKeywords];
    const preset = allPresets.find(p => p.pattern === pattern);

    if (preset) {
      const type = presetRegions.includes(preset) ? 'region'
        : presetProtocols.includes(preset) ? 'protocol' : 'keyword';
      selectedRules.value.push({ ...preset, type });
    } else {
      // 作为自定义关键字
      selectedRules.value.push({
        label: pattern,
        pattern: pattern,
        type: 'custom'
      });
    }
  });
};

// 切换模式时更新文本
watch(ruleMode, () => {
  syncToText();
});

// 监听弹窗显示，解析现有规则
watch(() => props.show, (newVal) => {
  if (newVal) {
    parseFromText();
  }
});

// === 高级模式相关（保留原有逻辑） ===
const excludeRuleTextarea = ref(null);
const excludeRuleHighlight = ref(null);

const excludeRuleLines = computed(() => {
  const text = props.editingSubscription?.exclude ?? '';
  const lines = text.split('\n');
  return lines.length ? lines : [''];
});

const excludeRuleState = computed(() => {
  const text = props.editingSubscription?.exclude ?? '';
  const rawLines = text.split('\n');
  const lines = rawLines.map(line => line.trim());
  const hasContent = lines.some(line => line);
  const dividerIndex = lines.findIndex(line => line === '---');
  const hasDivider = dividerIndex !== -1;
  const hasKeepPrefix = lines.some(line => line.toLowerCase().startsWith('keep:'));

  let tag = '未设置';
  if (hasContent) {
    if (hasDivider) tag = '混合';
    else if (hasKeepPrefix) tag = '仅包含';
    else tag = '排除';
  }

  const tagClassMap = {
    '未设置': 'bg-gray-100 text-gray-600 dark:bg-gray-700/50 dark:text-gray-300',
    '排除': 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-200',
    '仅包含': 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-200',
    '混合': 'bg-sky-100 text-sky-700 dark:bg-sky-900/30 dark:text-sky-200'
  };

  const errors = [];
  rawLines.forEach((rawLine, index) => {
    let line = rawLine.trim();
    if (!line) return;
    if (line === '---') return;

    if (line.toLowerCase().startsWith('keep:')) {
      line = line.substring('keep:'.length).trim();
      if (!line) {
        errors.push({ line: index + 1, message: 'keep: 后内容为空' });
        return;
      }
    }

    if (line.toLowerCase().startsWith('proto:')) {
      const protocols = line.substring('proto:'.length)
        .split(',')
        .map(p => p.trim())
        .filter(Boolean);
      if (protocols.length === 0) {
        errors.push({ line: index + 1, message: 'proto: 后未填写协议' });
      }
      return;
    }

    try {
      new RegExp(line);
    } catch (e) {
      errors.push({ line: index + 1, message: '正则无效' });
    }
  });

  return {
    tag,
    tagClass: tagClassMap[tag] || tagClassMap['未设置'],
    errors,
    errorsText: errors.map(item => `第${item.line}行：${item.message}`).join('；')
  };
});

const excludeRuleErrorLines = computed(() => new Set(
  excludeRuleState.value.errors.map(item => item.line)
));
const probeSummary = computed(() => getSourceProbeSummary(props.editingSubscription));
const isReprobing = ref(false);
const canReprobe = computed(() => {
  const normalized = normalizeSourceItem(props.editingSubscription);
  return canManuallyProbeSource(normalized);
});
const reprobeButtonTitle = computed(() => {
  if (canReprobe.value) return '重新执行一次辅助探测';
  return '仅支持对 http(s) 订阅或住宅代理式输入重新探测';
});
const lastProbeAtText = computed(() => {
  const timestamp = normalizeSourceItem(props.editingSubscription).last_probe_at;
  if (!timestamp) return '';
  return `${formatDate(timestamp, 'relative')} (${formatDate(timestamp, 'datetime')})`;
});
const detectedKindText = computed(() => {
  const detectedKind = normalizeSourceItem(props.editingSubscription).detected_kind;
  if (detectedKind === 'subscription') return '探测更像订阅';
  if (detectedKind === 'proxy_uri') return '探测更像直连代理';
  if (detectedKind === 'connector') return '探测更像连接器';
  return '';
});

const syncExcludeRuleScroll = () => {
  if (!excludeRuleTextarea.value || !excludeRuleHighlight.value) return;
  excludeRuleHighlight.value.scrollTop = excludeRuleTextarea.value.scrollTop;
  excludeRuleHighlight.value.scrollLeft = excludeRuleTextarea.value.scrollLeft;
};

const handleReprobe = async () => {
  const normalized = normalizeSourceItem(props.editingSubscription);
  if (!normalized.input) {
    showToast('请先输入订阅链接', 'warning');
    return;
  }
  if (!canReprobe.value) {
    showToast('当前输入不需要联网探测', 'info');
    return;
  }

  isReprobing.value = true;
  try {
    const result = await probeSourceRequest(props.editingSubscription);
    if (!result?.success || !result?.data?.source) {
      throw new Error(result?.error || '重新探测失败');
    }

    Object.assign(props.editingSubscription, result.data.source);
    const refreshedSummary = getSourceProbeSummary(props.editingSubscription);
    const toastType = refreshedSummary.tone === 'danger'
      ? 'warning'
      : refreshedSummary.tone === 'warning'
        ? 'info'
        : 'success';
    showToast(`探测结果已刷新：${refreshedSummary.label}`, toastType);
  } catch (error) {
    showToast(`重新探测失败: ${error.message || '未知错误'}`, 'error');
  } finally {
    isReprobing.value = false;
  }
};

const handleConfirm = () => {
  if (isAdvancedMode.value && excludeRuleState.value.errors.length > 0) {
    showToast('包含/排除规则有误，请先修正', 'error');
    return;
  }
  emit('confirm');
};

// 切换到高级模式
const switchToAdvanced = () => {
  isAdvancedMode.value = true;
};

// 切换到可视化模式
const switchToVisual = () => {
  isAdvancedMode.value = false;
  parseFromText();
};
</script>

<template>
  <Modal v-if="editingSubscription" :show="show" size="2xl"
    :confirm-disabled="isAdvancedMode && excludeRuleState.errors.length > 0" confirm-button-title="请先修正规则"
    @update:show="emit('update:show', $event)" @confirm="handleConfirm">
    <template #title>
      <div class="flex items-center gap-3">
        <div class="p-2 misub-radius-lg bg-indigo-500/10">
          <!-- Standard RSS/Link Icon for Subscription -->
          <svg xmlns="http://www.w3.org/2000/svg" class="h-6 w-6 text-indigo-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
            <path stroke-linecap="round" stroke-linejoin="round" d="M13.828 10.172a4 4 0 00-5.656 0l-4 4a4 4 0 105.656 5.656l1.102-1.101m-.758-4.899a4 4 0 005.656 0l4-4a4 4 0 00-5.656-5.656l-1.1 1.1" />
          </svg>
        </div>
        <h3 class="text-lg font-bold text-gray-800 dark:text-white">
          {{ isNew ? '新增订阅' : '编辑订阅' }}
        </h3>
      </div>
    </template>
    <template #body>
      <div class="space-y-4">
        <EditForm :editing-subscription="editingSubscription" />

        <div class="misub-radius-lg border px-4 py-3 text-sm"
          :class="{
            'bg-amber-50/80 text-amber-700 border-amber-200 dark:bg-amber-500/10 dark:text-amber-200 dark:border-amber-500/20': probeSummary.tone === 'warning',
            'bg-red-50/80 text-red-700 border-red-200 dark:bg-red-500/10 dark:text-red-200 dark:border-red-500/20': probeSummary.tone === 'danger',
            'bg-emerald-50/80 text-emerald-700 border-emerald-200 dark:bg-emerald-500/10 dark:text-emerald-200 dark:border-emerald-500/20': probeSummary.tone === 'success',
            'bg-gray-50/80 text-gray-600 border-gray-200 dark:bg-white/5 dark:text-gray-300 dark:border-white/10': !['warning', 'danger', 'success'].includes(probeSummary.tone)
          }">
          <div class="font-semibold mb-1">{{ probeSummary.label }}</div>
          <div>{{ probeSummary.description }}</div>
          <div v-if="detectedKindText || lastProbeAtText" class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs opacity-80">
            <span v-if="detectedKindText">{{ detectedKindText }}</span>
            <span v-if="lastProbeAtText">最近探测：{{ lastProbeAtText }}</span>
          </div>
        </div>

        <RuleSection
          :editing-subscription="editingSubscription"
          :is-rule-expanded="isRuleExpanded"
          :is-advanced-mode="isAdvancedMode"
          :rule-mode="ruleMode"
          :custom-keyword="customKeyword"
          :selected-rules="selectedRules"
          :preset-regions="presetRegions"
          :preset-protocols="presetProtocols"
          :preset-keywords="presetKeywords"
          :exclude-rule-state="excludeRuleState"
          :toggle-tag="toggleTag"
          :is-selected="isSelected"
          :add-custom-keyword="addCustomKeyword"
          :remove-rule="removeRule"
          :switch-to-advanced="switchToAdvanced"
          :switch-to-visual="switchToVisual"
          @toggle-expand="isRuleExpanded = !isRuleExpanded"
          @update:rule-mode="ruleMode = $event"
          @update:custom-keyword="customKeyword = $event"
        />

        <AdvancedOptions :editing-subscription="editingSubscription" />
      </div>
    </template>
    <template #footer>
      <div class="flex w-full items-center justify-between gap-3">
        <button
          type="button"
          @click="handleReprobe"
          :disabled="!canReprobe || isReprobing"
          :title="reprobeButtonTitle"
          class="px-4 py-2 bg-white hover:bg-gray-50 dark:bg-gray-800 dark:hover:bg-gray-700 text-gray-700 dark:text-gray-200 font-semibold text-sm misub-radius-lg transition-colors border border-gray-200 dark:border-gray-700 disabled:opacity-60 disabled:cursor-not-allowed"
        >
          {{ isReprobing ? '探测中...' : '重新探测' }}
        </button>
        <div class="flex items-center gap-3">
          <button
            type="button"
            @click="emit('update:show', false)"
            class="px-4 py-2 bg-gray-200 hover:bg-gray-300 dark:bg-gray-700 dark:hover:bg-gray-600 text-gray-800 dark:text-gray-200 font-semibold text-sm misub-radius-lg transition-colors"
          >
            取消
          </button>
          <button
            type="button"
            @click="handleConfirm"
            :disabled="isReprobing || (isAdvancedMode && excludeRuleState.errors.length > 0)"
            :title="isAdvancedMode && excludeRuleState.errors.length > 0 ? '请先修正规则' : '确认'"
            class="px-4 py-2 bg-indigo-600 hover:bg-indigo-700 text-white font-semibold text-sm misub-radius-lg transition-colors disabled:bg-gray-400 dark:disabled:bg-gray-600 disabled:opacity-70 disabled:cursor-not-allowed"
          >
            确认
          </button>
        </div>
      </div>
    </template>
  </Modal>
</template>

 
