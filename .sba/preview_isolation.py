"""独立读回运行中的 preview 路由栅栏；无 Cookie、凭据或隐含管理写入。"""
from cloudflare_http import secure_session
from refresh_context import require


def verify_isolation(config):
    session = secure_session()
    try:
        for origin, path in [(config.worker_origin, '/'), (config.worker_origin, '/connect'),
                             (config.pages_origin, '/api/data'), (config.pages_origin, '/cron'),
                             (config.pages_origin, '/api/backup/restore')]:
            with session.get(origin + path, timeout=(10, 30), stream=True, allow_redirects=False,
                             headers={'Cache-Control': 'no-cache'}) as response:
                require(response.status_code == 403 and 'Set-Cookie' not in response.headers
                        and response.headers.get('Cache-Control') == 'no-store'
                        and response.raw.read(256) == b'Preview isolated')
    finally:
        session.close()
