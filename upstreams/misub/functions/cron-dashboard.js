/**
 * MiSub Cron Dashboard
 * Lightweight operator page for viewing and manually triggering cron jobs.
 * The page relies on the authenticated `/api/cron/*` endpoints.
 */

export async function onRequest(context) {
    if (context.request.method !== 'GET') {
        return new Response('Method not allowed', { status: 405 });
    }

    const html = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>MiSub Cron Dashboard</title>
    <style>
        :root {
            color-scheme: light dark;
            --bg: #f6f7fb;
            --card: #ffffff;
            --border: rgba(15, 23, 42, 0.08);
            --text: #0f172a;
            --muted: #64748b;
            --accent: #2563eb;
            --accent-hover: #1d4ed8;
            --success: #16a34a;
            --danger: #dc2626;
            --warning: #d97706;
        }

        @media (prefers-color-scheme: dark) {
            :root {
                --bg: #0b1220;
                --card: #111827;
                --border: rgba(255, 255, 255, 0.08);
                --text: #e5e7eb;
                --muted: #94a3b8;
                --accent: #3b82f6;
                --accent-hover: #60a5fa;
                --success: #22c55e;
                --danger: #f87171;
                --warning: #f59e0b;
            }
        }

        * { box-sizing: border-box; }
        body {
            margin: 0;
            font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
            background: var(--bg);
            color: var(--text);
            padding: 32px 16px;
        }
        .page {
            max-width: 1100px;
            margin: 0 auto;
        }
        .header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            gap: 16px;
            margin-bottom: 24px;
        }
        .header h1 {
            margin: 0;
            font-size: 28px;
        }
        .header p {
            margin: 6px 0 0;
            color: var(--muted);
        }
        .actions {
            display: flex;
            gap: 12px;
            flex-wrap: wrap;
        }
        button {
            border: 0;
            border-radius: 12px;
            padding: 12px 18px;
            font-size: 14px;
            font-weight: 600;
            cursor: pointer;
            transition: background .18s ease, transform .18s ease;
        }
        button.primary {
            background: var(--accent);
            color: white;
        }
        button.primary:hover { background: var(--accent-hover); }
        button.secondary {
            background: transparent;
            color: var(--text);
            border: 1px solid var(--border);
        }
        button:disabled {
            opacity: .6;
            cursor: not-allowed;
        }
        .grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
            gap: 16px;
            margin-bottom: 24px;
        }
        .card {
            background: var(--card);
            border: 1px solid var(--border);
            border-radius: 18px;
            padding: 18px;
            box-shadow: 0 8px 30px rgba(15, 23, 42, 0.06);
        }
        .label {
            color: var(--muted);
            font-size: 13px;
            margin-bottom: 10px;
        }
        .value {
            font-size: 28px;
            font-weight: 700;
        }
        .status {
            margin-bottom: 16px;
            padding: 14px 16px;
            border-radius: 14px;
            border: 1px solid var(--border);
            background: var(--card);
            color: var(--muted);
        }
        .status.ok { color: var(--success); }
        .status.warn { color: var(--warning); }
        .status.err { color: var(--danger); }
        .section-title {
            margin: 0 0 12px;
            font-size: 18px;
        }
        .meta {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
            gap: 12px;
            margin-bottom: 24px;
        }
        .meta-item {
            font-size: 14px;
            color: var(--muted);
        }
        .meta-item strong {
            display: block;
            color: var(--text);
            margin-bottom: 6px;
        }
        .logs {
            display: grid;
            gap: 10px;
        }
        .log {
            border-left: 4px solid var(--border);
            padding: 12px 14px;
            border-radius: 12px;
            background: rgba(127, 127, 127, 0.05);
        }
        .log.err { border-left-color: var(--danger); }
        .log .title {
            font-weight: 600;
            margin-bottom: 4px;
        }
        .log .detail {
            color: var(--muted);
            font-size: 13px;
            white-space: pre-wrap;
            word-break: break-word;
        }
    </style>
</head>
<body>
    <div class="page">
        <div class="header">
            <div>
                <h1>MiSub Cron Dashboard</h1>
                <p>查看最近一次自动任务执行状态，并在后台手动触发同步。</p>
            </div>
            <div class="actions">
                <button id="refreshBtn" class="secondary">刷新状态</button>
                <button id="triggerBtn" class="primary">手动触发</button>
            </div>
        </div>

        <div id="statusBanner" class="status">正在加载状态...</div>

        <div class="grid">
            <div class="card">
                <div class="label">总订阅数</div>
                <div id="totalSubs" class="value">-</div>
            </div>
            <div class="card">
                <div class="label">成功同步</div>
                <div id="successCount" class="value">-</div>
            </div>
            <div class="card">
                <div class="label">失败同步</div>
                <div id="failedCount" class="value">-</div>
            </div>
            <div class="card">
                <div class="label">最近一次运行</div>
                <div id="lastSync" class="value" style="font-size:20px">-</div>
            </div>
        </div>

        <div class="card" style="margin-bottom: 24px;">
            <h2 class="section-title">执行配置</h2>
            <div class="meta">
                <div class="meta-item"><strong>触发类型</strong><span id="cronType">-</span></div>
                <div class="meta-item"><strong>最大同步数</strong><span id="maxSyncCount">-</span></div>
                <div class="meta-item"><strong>单次超时</strong><span id="syncTimeout">-</span></div>
                <div class="meta-item"><strong>并行模式</strong><span id="parallelMode">-</span></div>
                <div class="meta-item"><strong>需要 Secret</strong><span id="requiresSecret">-</span></div>
                <div class="meta-item"><strong>Aggregator Cron</strong><span id="aggregatorCron">-</span></div>
            </div>
        </div>

        <div class="card">
            <h2 class="section-title">失败明细</h2>
            <div id="logs" class="logs">
                <div class="log"><div class="detail">暂无执行记录。</div></div>
            </div>
        </div>
    </div>

    <script>
        const statusBanner = document.getElementById('statusBanner');
        const refreshBtn = document.getElementById('refreshBtn');
        const triggerBtn = document.getElementById('triggerBtn');

        function setBanner(message, kind = '') {
            statusBanner.className = 'status' + (kind ? ' ' + kind : '');
            statusBanner.textContent = message;
        }

        function setText(id, value) {
            document.getElementById(id).textContent = value ?? '-';
        }

        function formatTime(value) {
            if (!value) return '从未';
            const date = new Date(value);
            if (Number.isNaN(date.getTime())) return value;
            return date.toLocaleString('zh-CN');
        }

        function renderLogs(details) {
            const container = document.getElementById('logs');
            container.innerHTML = '';

            if (!Array.isArray(details) || details.length === 0) {
                container.innerHTML = '<div class="log"><div class="detail">没有失败记录。</div></div>';
                return;
            }

            for (const item of details) {
                const row = document.createElement('div');
                row.className = 'log err';
                row.innerHTML = '<div class="title"></div><div class="detail"></div>';
                row.querySelector('.title').textContent = item.name || '未知订阅';
                row.querySelector('.detail').textContent = item.error || '未知错误';
                container.appendChild(row);
            }
        }

        async function loadStatus() {
            setBanner('正在加载状态...');
            try {
                const response = await fetch('/api/cron/status', {
                    credentials: 'same-origin'
                });

                if (response.status === 401) {
                    setBanner('当前未登录或会话已过期，请先登录 MiSub 后再访问此页面。', 'warn');
                    return;
                }

                if (!response.ok) {
                    const text = await response.text();
                    throw new Error(text || 'Failed to fetch cron status');
                }

                const data = await response.json();
                setBanner(data.enabled ? 'Cron 管理已启用。' : 'Cron Secret 尚未配置，外部调度不会生效。', data.enabled ? 'ok' : 'warn');

                setText('totalSubs', data.totalSubscriptions ?? 0);
                setText('successCount', data.successfulSyncs ?? 0);
                setText('failedCount', data.failedSyncs ?? 0);
                setText('lastSync', formatTime(data.lastSync));

                setText('cronType', data.config?.type ?? '-');
                setText('maxSyncCount', data.config?.maxSyncCount ?? '未设置');
                setText('syncTimeout', data.config?.syncTimeout ? data.config.syncTimeout + ' ms' : '未设置');
                setText('parallelMode', data.config?.enableParallel === null ? '未设置' : (data.config.enableParallel ? '开启' : '关闭'));
                setText('requiresSecret', data.config?.requiresSecret ? '是' : '否');
                setText('aggregatorCron', data.config?.aggregatorRunOnCron ? '开启' : '关闭');

                renderLogs(data.details);
            } catch (error) {
                console.error('[Cron Dashboard] loadStatus failed:', error);
                setBanner('加载状态失败：' + error.message, 'err');
            }
        }

        async function triggerSync() {
            triggerBtn.disabled = true;
            setBanner('正在触发同步...', 'warn');
            try {
                const response = await fetch('/api/cron/trigger', {
                    method: 'POST',
                    credentials: 'same-origin'
                });

                if (response.status === 401) {
                    setBanner('当前未登录或会话已过期，请重新登录后再试。', 'warn');
                    return;
                }

                const data = await response.json();
                if (!response.ok || data.success === false) {
                    throw new Error(data.error || 'Cron trigger failed');
                }

                setBanner('手动触发已完成。', 'ok');
                await loadStatus();
            } catch (error) {
                console.error('[Cron Dashboard] trigger failed:', error);
                setBanner('手动触发失败：' + error.message, 'err');
            } finally {
                triggerBtn.disabled = false;
            }
        }

        refreshBtn.addEventListener('click', loadStatus);
        triggerBtn.addEventListener('click', triggerSync);

        loadStatus();
        setInterval(loadStatus, 30000);
    </script>
</body>
</html>`;

    return new Response(html, {
        headers: { 'Content-Type': 'text/html; charset=utf-8' }
    });
}
