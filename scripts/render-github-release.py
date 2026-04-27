#!/usr/bin/env python3

from __future__ import annotations

import argparse
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser(description="Render a GitHub release body for EasyProxy.")
    parser.add_argument("--tag", required=True)
    parser.add_argument("--owner", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--service-image-name", default="easy-proxy-monorepo-service")
    parser.add_argument("--worker-image-name", default="ech-workers-monorepo")
    parser.add_argument("--misub-url", default="https://misub.aiaimimi.com")
    parser.add_argument("--aggregator-url", default="https://sub.aiaimimi.com")
    parser.add_argument("--ech-worker-url", default="https://proxyservice-ech-workers.aiaimimi.com")
    args = parser.parse_args()

    body = f"""# EasyProxy {args.tag}

## Summary

This release packages the current single-repository EasyProxy operator surface:

- native `aggregator` publishing
- native `MiSub` Pages deployment
- native `ech-workers-cloudflare` deployment
- GHCR images for `service/base` and local `ech-workers`
- private `service/base` config distribution through Cloudflare R2

## Published Images

- `ghcr.io/{args.owner}/{args.service_image_name}:{args.tag}`
- `ghcr.io/{args.owner}/{args.worker_image_name}:{args.tag}`

## Public Endpoints

- Aggregator artifacts: `{args.aggregator_url}`
- MiSub: `{args.misub_url}`
- ECH Worker: `{args.ech_worker_url}`

## Recommended Operator Flow

1. Publish GHCR images:
   - `Publish GHCR Images`
2. Publish `service/base` private config:
   - `Publish Service Base Config`
3. Deploy Cloudflare apps:
   - `Deploy Cloudflare Apps`
4. Refresh aggregator artifacts:
   - `Deploy Aggregator`

## Config Distribution

The `service/base` image can bootstrap from:

- `EASY_PROXY_IMPORT_CODE`
- `/etc/easy-proxy/bootstrap/r2-bootstrap.json`

See:

- `docs/service-base-config-distribution.md`
- `docs/release-checklist.md`

## Validation Baseline

The repository validation workflow remains:

- `Validate`

For a full release, confirm the latest successful runs of:

- `Validate`
- `Deploy Aggregator`
- `Deploy Cloudflare Apps`
- `Publish GHCR Images`
- `Publish Service Base Config`
"""

    output_path = Path(args.output)
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(body.rstrip() + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
