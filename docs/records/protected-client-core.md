# Protected proxy identity core (not deployed)

This change adds an opt-in dispatch.Config.ProtectedClients map. Each exact
proxy username owns a separate proxy password and one required node tag.
The tag is server-controlled. Username suffixes and empty bindings are rejected;
path/header routing parameters cannot change this required node or select DIRECT.
Wrong credentials fail authentication. Ordinary authenticated clients retain their
existing rules. A protected-only dispatcher rejects unknown/anonymous clients.

This is currently a programmatic dispatcher capability, not a runtime YAML/API
feature. The production legacy listener has NOT been replaced. No NAS service,
Linux proxy setting, network namespace, firewall, or route has been changed.
A separate compatibility-reviewed runtime wiring step is required before use.
Do not put sample credentials or real node URIs into this repository.

The core reuses pool.RequiredTag filtering. Missing/unavailable nodes must fail
rather than selecting another node. This is a single-route constraint, NOT a
static-public-IP guarantee. A provider can change its public exit behind a node.

Coverage is limited to requests authenticated through this dispatcher. It does
not identify remote operating-system processes, decrypt TLS, classify Claude by
User-Agent, or prevent a client from bypassing the proxy. Requests deliberately
using an ordinary credential follow ordinary policy.

Suggested runtime integration: route explicit HTTP/CONNECT authentication through
this dispatcher in the existing EasyProxy process, preserving the legacy inbound
for all non-protected clients where possible. Do not automatically enable Local
Server, shared routing rules, or TUN to achieve that integration. Changes to
listener ownership, restart/reload behavior, and ordinary mixed-protocol clients
need independent integration tests and deployment approval.
