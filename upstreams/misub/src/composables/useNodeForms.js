import { ref } from "vue";
import { useToastStore } from "../stores/toast.js";
import { extractNodeName } from "../lib/utils.js";
import { generateNodeId } from "../utils/id.js";
import {
  isConnectorSource,
  isProxyURISource,
  normalizeSourceItem,
  SOURCE_CONNECTOR_TYPE_ECH_WORKER,
  SOURCE_CONNECTOR_TYPE_ZENPROXY_CLIENT,
} from "../shared/source-utils.js";

const isDev = import.meta.env.DEV;

export function useNodeForms({ addNode, updateNode }) {
  const { showToast } = useToastStore();
  const showModal = ref(false);
  const isNew = ref(false);
  const editingNode = ref(null);

  const openAdd = () => {
    isNew.value = true;
    editingNode.value = {
      id: generateNodeId(),
      name: "",
      url: "",
      input: "",
      kind: "proxy_uri",
      enabled: true,
      colorTag: null,
      group: "",
      connector_type: SOURCE_CONNECTOR_TYPE_ECH_WORKER,
      connector_config: {},
    };
    showModal.value = true;
  };

  const openEdit = (node) => {
    if (!node) {
      console.error("UseNodeForms: openEdit called with null");
      return;
    }
    if (isDev) {
      console.debug("UseNodeForms: openEdit called with", node);
    }
    isNew.value = false;
    editingNode.value = { ...node };
    if (isDev) {
      console.debug("UseNodeForms: editingNode set to", editingNode.value);
    }
    showModal.value = true;
  };

  const handleUrlInput = (event) => {
    if (!editingNode.value) return;
    const newUrl = event.target.value;
    if (newUrl && !editingNode.value.name) {
      editingNode.value.name = extractNodeName(newUrl);
    }
  };

  const handleSave = () => {
    if (!editingNode.value || !editingNode.value.url) {
      showToast("节点链接不能为空", "error");
      return;
    }
    const normalized = normalizeSourceItem(editingNode.value);
    if (isConnectorSource(normalized)) {
      if (!/^https?:\/\//i.test(normalized.input)) {
        const label =
          normalized.connector_type === SOURCE_CONNECTOR_TYPE_ZENPROXY_CLIENT
            ? "ZenProxy"
            : "ECH Worker";
        showToast(`${label} 连接器请输入有效的 HTTP/HTTPS 地址`, "error");
        return;
      }
      if (normalized.connector_type === SOURCE_CONNECTOR_TYPE_ZENPROXY_CLIENT) {
        if (!String(normalized.connector_config?.api_key || "").trim()) {
          showToast("ZenProxy 连接器缺少 API Key", "error");
          return;
        }
        const count = Number(normalized.connector_config?.count || 0);
        if (!Number.isFinite(count) || count <= 0) {
          showToast("ZenProxy 连接器的 count 必须大于 0", "error");
          return;
        }
      }
    } else if (!isProxyURISource(normalized)) {
      showToast("请输入有效的节点链接或代理地址", "error");
      return;
    }

    if (isNew.value) {
      addNode(normalized);
    } else {
      updateNode(normalized);
    }
    showModal.value = false;
  };

  return {
    showModal,
    isNew,
    editingNode,
    openAdd,
    openEdit,
    handleUrlInput,
    handleSave,
  };
}
