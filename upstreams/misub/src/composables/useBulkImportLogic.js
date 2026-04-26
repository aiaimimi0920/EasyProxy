import { ref } from 'vue';
import { useToastStore } from '../stores/toast.js';
import { extractNodeName } from '../lib/utils.js';
import { generateNodeId, generateSubscriptionId } from '../utils/id.js';
import { COMMON_NODE_PROTOCOLS, createProtocolRegex } from '@/constants/nodeProtocols.js';
import {
    SOURCE_KIND_PROXY_URI,
    SOURCE_KIND_SUBSCRIPTION,
    inferSourceKind,
    normalizeDirectProxyInput
} from '../shared/source-utils.js';

const BULK_IMPORT_NODE_PROTOCOLS = COMMON_NODE_PROTOCOLS;
const BULK_IMPORT_NODE_REGEX = createProtocolRegex(BULK_IMPORT_NODE_PROTOCOLS, false);

export function useBulkImportLogic({ addSubscriptionsFromBulk, addNodesFromBulk }) {
    const { showToast } = useToastStore();
    const showModal = ref(false);

    const handleBulkImport = (importText, group) => {
        if (!importText) return;

        const lines = importText.split('\n').map(line => line.trim()).filter(Boolean);
        const validSubs = [];
        const validNodes = [];

        lines.forEach(line => {
            const normalizedInput = normalizeDirectProxyInput(line);
            const inferredKind = inferSourceKind(normalizedInput);
            const baseItem = {
                name: extractNodeName(normalizedInput) || '未命名',
                input: normalizedInput,
                url: normalizedInput,
                kind: inferredKind,
                enabled: true,
                status: 'unchecked',
                group: group || null,
                colorTag: null,
                // Default fields for subscriptions
                exclude: '',
                customUserAgent: '',
                notes: ''
            };

            if (inferredKind === SOURCE_KIND_SUBSCRIPTION) {
                validSubs.push({ ...baseItem, id: generateSubscriptionId() });
            } else if (inferredKind === SOURCE_KIND_PROXY_URI && BULK_IMPORT_NODE_REGEX.test(normalizedInput)) {
                validNodes.push({ ...baseItem, id: generateNodeId() });
            }
        });

        let message = '';

        if (validSubs.length > 0) {
            addSubscriptionsFromBulk(validSubs);
            message += `成功导入 ${validSubs.length} 条订阅 `;
        }

        if (validNodes.length > 0) {
            addNodesFromBulk(validNodes);
            message += `成功导入 ${validNodes.length} 个节点`;
        }

        if (message) {
            showToast(message, 'success');
        } else {
            showToast('未检测到有效的链接', 'warning');
        }
        showModal.value = false;
    };

    return {
        showModal,
        handleBulkImport
    };
}
