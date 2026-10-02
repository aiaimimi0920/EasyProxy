# Local sing-tun patch

This directory preserves `github.com/sagernet/sing-tun v0.7.13`, including its
upstream license. The sole source change opens the Linux TUN device with
`O_CLOEXEC` atomically.

Without this flag, subprocesses started by EasyProxy inherit the TUN descriptor.
After the parent closes its box during reload, an `ech-workers` child can retain
the interface and make the replacement box fail with `device or resource busy`.
Setting the flag after opening leaves a race with concurrent process creation.

The local module replacement applies to ordinary Go builds and Docker builds.
Keep the version pinned until an upstream version supplies this fix, then remove
the replacement and this directory together. The Linux regression lives in
`internal/tuncheck` and requires `/dev/net/tun` plus `CAP_NET_ADMIN`; run it in an
isolated network namespace, never against the shared gateway namespace.
