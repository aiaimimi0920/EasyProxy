#!/usr/bin/env python3
"""从本次 mixed 候选生成 V2Ray 兼容订阅，不写入 stable。"""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re

import boto3


SUPPORTED_SCHEMES = frozenset({'vmess', 'vless', 'trojan', 'ss', 'ssr'})
MAX_BODY_BYTES = 16 * 1024 * 1024
CANDIDATE_KEY = re.compile(r'^candidate/([A-Za-z0-9][A-Za-z0-9._-]{0,127})/[^\s]+$')
URI_LINE = re.compile(r'^([a-z][a-z0-9+.-]*)://\S+$')


def build_payload(content: bytes) -> tuple[bytes, dict]:
    if not content.strip() or len(content) > MAX_BODY_BYTES:
        raise RuntimeError('mixed candidate is empty or exceeds the size limit')
    try:
        encoded = b''.join(content.split())
        decoded = base64.b64decode(encoded + b'=' * (-len(encoded) % 4), validate=True).decode('utf-8')
    except (ValueError, UnicodeError) as exc:
        raise RuntimeError('mixed candidate is not a UTF-8 Base64 subscription') from exc

    selected, seen, counts, skipped = [], set(), {}, 0
    for raw in decoded.splitlines():
        line = raw.strip()
        if not line:
            continue
        match = URI_LINE.fullmatch(line)
        if not match:
            raise RuntimeError('mixed candidate contains a malformed URI line')
        scheme = match.group(1)
        if scheme not in SUPPORTED_SCHEMES:
            skipped += 1
            continue
        if line not in seen:
            selected.append(line)
            seen.add(line)
            counts[scheme] = counts.get(scheme, 0) + 1
    if not selected:
        raise RuntimeError('mixed candidate contains no V2Ray-compatible proxy URIs')
    body = base64.b64encode(('\n'.join(selected) + '\n').encode('utf-8'))
    return body, {'count': len(selected), 'schemes': counts, 'skipped': skipped,
                  'bytes': len(body), 'sha256': hashlib.sha256(body).hexdigest()}


def candidate_items(runtime: dict) -> tuple[dict, dict]:
    items = runtime.get('storage', {}).get('items', {})
    selected = []
    runs = []
    for name in ('public-mixed', 'public-v2ray'):
        item = items.get(name)
        if not isinstance(item, dict) or not str(item.get('bucket') or '').strip():
            raise RuntimeError(f'{name}: missing bucket configuration')
        key = str(item.get('key') or '')
        match = CANDIDATE_KEY.fullmatch(key)
        if not match or any(part in ('', '.', '..') for part in key.split('/')):
            raise RuntimeError(f'{name}: expected an isolated candidate run key')
        selected.append(item)
        runs.append(match.group(1))
    source, target = selected
    if runs[0] != runs[1] or source['bucket'] != target['bucket'] or source['key'] == target['key']:
        raise RuntimeError('mixed and V2Ray must be distinct objects in the same candidate run and bucket')
    return source, target


def publish(s3, runtime: dict) -> dict:
    source, target = candidate_items(runtime)
    response = s3.get_object(Bucket=source['bucket'], Key=source['key'])
    stream = response['Body']
    try:
        content = stream.read(MAX_BODY_BYTES + 1)
    finally:
        stream.close()
    body, summary = build_payload(content)
    s3.put_object(Bucket=target['bucket'], Key=target['key'], Body=body,
                  ContentType='text/plain; charset=utf-8', CacheControl='max-age=60')
    return summary


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--runtime-config', required=True)
    args = parser.parse_args()
    runtime = json.loads(Path(args.runtime_config).read_text(encoding='utf-8'))
    candidate_items(runtime)
    names = ('R2_ACCESS_KEY_ID', 'R2_SECRET_ACCESS_KEY', 'R2_ACCOUNT_ID')
    access, secret, account = (os.environ.get(name, '').strip() for name in names)
    if not all((access, secret, account)):
        raise RuntimeError('R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, and R2_ACCOUNT_ID are required')
    client = boto3.client('s3', endpoint_url=f'https://{account}.r2.cloudflarestorage.com',
                          aws_access_key_id=access, aws_secret_access_key=secret, region_name='auto')
    print(json.dumps(publish(client, runtime), sort_keys=True))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
