import { describe, expect, it } from 'vitest';
import { applyNodeTransformPipeline } from '../../functions/utils/node-transformer.js';

function buildVmessNode({ name, host, port }) {
  const payload = {
    v: '2',
    ps: name,
    add: host,
    port: String(port),
    id: '00000000-0000-0000-0000-000000000001',
    aid: 0,
    net: 'tcp',
    type: 'none',
    host: '',
    path: '',
    tls: 'tls'
  };

  return `vmess://${Buffer.from(JSON.stringify(payload), 'utf8').toString('base64')}`;
}

function getNodeName(node) {
  const hashIndex = node.lastIndexOf('#');
  if (hashIndex !== -1) {
    return decodeURIComponent(node.slice(hashIndex + 1));
  }

  if (node.startsWith('vmess://')) {
    const encoded = node.slice('vmess://'.length);
    return JSON.parse(Buffer.from(encoded, 'base64').toString('utf8')).ps;
  }

  return '';
}

describe('applyNodeTransformPipeline', () => {
  it('rewrites standard URL fragments when regex rename is enabled', () => {
    const result = applyNodeTransformPipeline([
      'trojan://password@hk1.example.com:443#HK%20Node%2001'
    ], {
      enabled: true,
      rename: {
        regex: {
          enabled: true,
          rules: [{ pattern: 'Node', replacement: 'Proxy', flags: 'g' }]
        },
        template: { enabled: false }
      },
      dedup: { enabled: false, mode: 'serverPort', includeProtocol: false, prefer: { protocolOrder: [] } },
      sort: { enabled: false, keys: [] }
    });

    expect(result).toHaveLength(1);
    expect(getNodeName(result[0])).toBe('HK Proxy 01');
  });

  it('applies template renaming per region and protocol scope', () => {
    const result = applyNodeTransformPipeline([
      'trojan://password@hk1.example.com:443#Hong%20Kong%2001',
      'trojan://password@hk2.example.com:443#Hong%20Kong%2002',
      'trojan://password@us1.example.com:443#US%20Node%2001'
    ], {
      enabled: true,
      rename: {
        regex: { enabled: false, rules: [] },
        template: {
          enabled: true,
          template: '{emoji}{region}-{protocol}-{index}',
          indexStart: 1,
          indexPad: 2,
          indexScope: 'regionProtocol',
          regionAlias: {},
          protocolAlias: {}
        }
      },
      dedup: { enabled: false, mode: 'serverPort', includeProtocol: false, prefer: { protocolOrder: [] } },
      sort: { enabled: false, keys: [] }
    });

    expect(result.map(getNodeName)).toEqual([
      '🇭🇰HK-trojan-01',
      '🇭🇰HK-trojan-02',
      '🇺🇸US-trojan-01'
    ]);
  });

  it('prefers protocols according to dedup priority on identical server and port', () => {
    const result = applyNodeTransformPipeline([
      buildVmessNode({ name: 'US VMess', host: 'shared.example.com', port: 443 }),
      'trojan://password@shared.example.com:443#HK%20Trojan'
    ], {
      enabled: true,
      rename: {
        regex: { enabled: false, rules: [] },
        template: { enabled: false }
      },
      dedup: {
        enabled: true,
        mode: 'serverPort',
        includeProtocol: false,
        prefer: { protocolOrder: ['trojan', 'vmess'] }
      },
      sort: { enabled: false, keys: [] }
    });

    expect(result).toHaveLength(1);
    expect(result[0].startsWith('trojan://')).toBe(true);
    expect(getNodeName(result[0])).toBe('HK Trojan');
  });

  it('sorts by explicit keys after transformation', () => {
    const result = applyNodeTransformPipeline([
      'trojan://password@node-b.example.com:8443#Node%20B',
      'trojan://password@node-a.example.com:443#Node%20A',
      'trojan://password@node-c.example.com:9443#Node%20C'
    ], {
      enabled: true,
      rename: {
        regex: { enabled: false, rules: [] },
        template: { enabled: false }
      },
      dedup: { enabled: false, mode: 'serverPort', includeProtocol: false, prefer: { protocolOrder: [] } },
      sort: {
        enabled: true,
        nameIgnoreEmoji: true,
        keys: [{ key: 'port', order: 'asc' }]
      }
    });

    expect(result.map(getNodeName)).toEqual(['Node A', 'Node B', 'Node C']);
  });
});
