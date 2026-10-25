"""审计摘要的日志白名单，不转发异常文本、地址或节点凭据。"""

FAILURE_STAGES = frozenset({
    'management-ready', 'scenario-ready', 'node-discovery', 'direct-probe',
    'pool-probe', 'lease-probe', 'validation', 'summary-write',
})


def safe_audit_details(result):
    nodes = result.get('nodes')
    nodes = nodes if isinstance(nodes, dict) else {}

    def count(value):
        return value if type(value) is int and 0 <= value <= 10000000 else None

    stable = nodes.get('stable_available_uris')
    stable_count = len(stable) if isinstance(stable, list) else nodes.get('stable_available_count')
    pool = result.get('pool_probe')
    attempts = pool.get('attempts') if isinstance(pool, dict) else None
    stage = result.get('failure_stage')
    return {
        'failure_stage': stage if isinstance(stage, str) and stage in FAILURE_STAGES else 'unknown',
        'total_nodes': count(nodes.get('total_nodes')),
        'available_nodes': count(nodes.get('available_nodes')),
        'stable_available_count': count(stable_count),
        'pool_attempt_count': count(len(attempts)) if isinstance(attempts, list) else None,
    }
