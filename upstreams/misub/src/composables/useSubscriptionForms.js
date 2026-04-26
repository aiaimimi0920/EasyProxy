import { ref } from 'vue';
import { useToastStore } from '../stores/toast.js';
import { generateSubscriptionId } from '../utils/id.js';
import { isSubscriptionSource, normalizeSourceItem } from '../shared/source-utils.js';

const isDev = import.meta.env.DEV;

export function useSubscriptionForms({ addSubscription, updateSubscription }) {
    const { showToast } = useToastStore();
    const showModal = ref(false);
    const isNew = ref(false);
    const editingSubscription = ref(null);

    const openAdd = () => {
        isNew.value = true;
        editingSubscription.value = {
            name: '',
            url: '',
            input: '',
            kind: 'subscription',
            enabled: true,
            exclude: '',
            customUserAgent: 'MiSub',
            fetchProxy: '',
            plusAsSpace: false,
            notes: ''
        };
        showModal.value = true;
    };

    const openEdit = (sub) => {
        if (!sub) {
            console.error('UseSubscriptionForms: openEdit called with null/undefined');
            return;
        }
        if (isDev) {
            console.debug('UseSubscriptionForms: openEdit called with', sub);
        }
        isNew.value = false;
        // Deep copy to avoid mutating store state directly before save
        try {
            editingSubscription.value = JSON.parse(JSON.stringify(sub));
            if (isDev) {
                console.debug('UseSubscriptionForms: editingSubscription set to', editingSubscription.value);
            }
            showModal.value = true;
        } catch (e) {
            console.error('UseSubscriptionForms: Failed to clone subscription', e);
        }
    };

    const handleSave = () => {
        if (!editingSubscription.value || !editingSubscription.value.url) {
            showToast('订阅链接不能为空', 'error');
            return;
        }
        const normalized = normalizeSourceItem(editingSubscription.value);
        if (!isSubscriptionSource(normalized)) {
            showToast('请输入有效的 http:// 或 https:// 订阅链接', 'error');
            return;
        }

        if (isNew.value) {
            addSubscription({ ...normalized, id: generateSubscriptionId() });
        } else {
            updateSubscription(normalized);
        }
        showModal.value = false;
    };

    return {
        showModal,
        isNew,
        editingSubscription,
        openAdd,
        openEdit,
        handleSave
    };
}
