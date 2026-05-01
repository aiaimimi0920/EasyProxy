import { describe, expect, it } from "vitest";
import {
  SOURCE_CONNECTOR_TYPE_ECH_WORKER,
  SOURCE_CONNECTOR_TYPE_ZENPROXY_CLIENT,
  SOURCE_KIND_CONNECTOR,
  SOURCE_KIND_PROXY_URI,
  normalizeSourceItem,
  toManifestSource,
} from "../../src/shared/source-utils.js";

describe("source-utils connector helpers", () => {
  it("normalizes zenproxy connector records while preserving connector metadata", () => {
    const normalized = normalizeSourceItem({
      id: "zenproxy-1",
      kind: SOURCE_KIND_CONNECTOR,
      name: "ZenProxy Provider",
      input: "https://zenproxy.top",
      connector_type: SOURCE_CONNECTOR_TYPE_ZENPROXY_CLIENT,
      connector_config: {
        api_key: "demo-key",
        count: 20,
        country: "US",
      },
    });

    expect(normalized.kind).toBe(SOURCE_KIND_CONNECTOR);
    expect(normalized.input).toBe("https://zenproxy.top");
    expect(normalized.connector_type).toBe(
      SOURCE_CONNECTOR_TYPE_ZENPROXY_CLIENT,
    );
    expect(normalized.connector_config).toMatchObject({
      api_key: "demo-key",
      count: 20,
      country: "US",
    });
    expect(normalized.options).toMatchObject({
      connector_type: SOURCE_CONNECTOR_TYPE_ZENPROXY_CLIENT,
      connector_config: expect.objectContaining({
        api_key: "demo-key",
      }),
    });
  });

  it("keeps ordinary proxy_uri inputs out of connector mode", () => {
    const normalized = normalizeSourceItem({
      id: "proxy-1",
      kind: SOURCE_KIND_PROXY_URI,
      input: "user:pass@proxy.example.com:8080",
    });

    expect(normalized.kind).toBe(SOURCE_KIND_PROXY_URI);
    expect(normalized.connector_type).toBe("");
    expect(normalized.input).toBe("http://user:pass@proxy.example.com:8080");
  });

  it("exports zenproxy connectors into manifest options unchanged", () => {
    const manifest = toManifestSource({
      id: "connector-zenproxy",
      kind: SOURCE_KIND_CONNECTOR,
      name: "ZenProxy Provider",
      input: "https://zenproxy.top",
      connector_type: SOURCE_CONNECTOR_TYPE_ZENPROXY_CLIENT,
      connector_config: {
        api_key: "demo-key",
        count: 12,
        type: "vmess",
      },
    });

    expect(manifest).toMatchObject({
      id: "connector-zenproxy",
      kind: SOURCE_KIND_CONNECTOR,
      input: "https://zenproxy.top",
      options: {
        connector_type: SOURCE_CONNECTOR_TYPE_ZENPROXY_CLIENT,
        connector_config: {
          api_key: "demo-key",
          count: 12,
          type: "vmess",
        },
      },
    });
  });

  it("keeps ech_worker as the legacy connector type", () => {
    const normalized = normalizeSourceItem({
      id: "connector-ech",
      kind: SOURCE_KIND_CONNECTOR,
      input: "https://ech.example.com/connect",
      connector_type: SOURCE_CONNECTOR_TYPE_ECH_WORKER,
    });

    expect(normalized.connector_type).toBe(SOURCE_CONNECTOR_TYPE_ECH_WORKER);
  });
});
