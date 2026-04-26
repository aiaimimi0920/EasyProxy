# ech-workers Local Runtime Note

There is no standalone deployment bundle for `ech-workers` in this monorepo.

Current runtime ownership:

- source code:
  - `upstreams/ech-workers`
- primary local host:
  - `EasyProxy`
- packaged binary path inside the EasyProxy image:
  - `/usr/local/bin/ech-workers`

Operational rule:

- deploy `ech-workers` as part of the `EasyProxy` container image
- manage worker URL and access token through:
  - local `EasyProxy` connector config
  - or `MiSub` manifest connector sources

Do not maintain a second, separate container contract for `ech-workers`
unless the architecture changes explicitly.
