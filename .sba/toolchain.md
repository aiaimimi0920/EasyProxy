# 固定 Linux 工具链与验证边界

## 交付范围

新套件使用 SpringBok 的 Ubuntu 24.04/x64 执行合同，不建立第四个常驻服务、不二次派发
Linux 任务，也不跨进程序列化共同 Permit。本目录目前仍无完整五动作入口和可部署声明；
本文的本机实测不代表 EasyProxy 已通过 SpringBok 上线。

`toolchain.py` 固定官方 Node **22.23.3**、独立 npm **10.9.7**、esbuild **0.28.2**、Go
**1.27.3** 的准确包 URL、SHA256、平台与版本输出。安装到全新源码外目录，执行位显式设置，
HOME/npm/cache/Go 构建环境隔离，子进程不继承云写凭据或用户注入配置。Pages 继续使用批准
MiSub lock 的全部版本与 integrity；仅在副本将历史镜像 URL 改为官方等价路径。

## Python 与原生生成器

`requirements-linux.lock` 包含 28 个 CPython 3.12/Linux x64 可用的官方 PyPI wheel URL 与
SHA256，锁文件本身也有准确 SHA gate。`python_runtime.py` 建立私有全新 `venv --copies`，
禁止 system site packages，以 `--isolated --no-index --require-hashes --only-binary=:all:`
和 `--ignore-installed` 安装，末尾 `pip check`。不接受环境代理、pip 配置、Python 注入
或云凭据；无失败目录复用/自动恢复。测试运行器的 pytest 依赖不属于产品运行时。

Aggregator 仅接受 gitlink `089fb85414efff4c7deccfda48b5a540ce536835`，两个原生工具先校验
全部字节后才赋执行权限，生成后再次核验。摘要来自批准 gitlink 内实际文件，**不是另行
验证过的官方 release 签名或 provenance**。实际离线 fixture 已执行 mihomo 版本命令，并
通过 subconverter 将 base64 订阅转换为含预期节点的 Clash 输出。

隔离副本还固定修补 `subscribe/workflow.py` 的订阅任务边界：某个 `execute()` 的普通
异常只产生该 taskid 的空结果，不再让 `pool.map()` 清空其他成功任务。不重试、不捕获
取消、不记录异常/URL；任务顺序与数量保持。TLS 和任务隔离两个补丁的修改前后完整
SHA256 都绑定在代码中，修改前检查全部源，生成后要求恰好两个固定 provenance 项并
重核实际文件；不信任 receipt 自报的摘要，不改独立 upstream，也不放宽发布审计。

## TLS 的三个层次

1. **源码抓取 HTTPS**：对批准 `subscribe/utils.py` 的准确 LF Git blob 做版本化最小补丁，
   删除 `check_hostname=False` 和 `CERT_NONE`，保留 `ssl.create_default_context()`。不改独立
   upstream。每个 group 明确要求 `regularize.enable=False`，避免浮动 MMDB 和未修的旁路。
2. **可用性探针目标 HTTPS**：容器 Python probe 使用默认验证 context；宿主 curl 使用
   `-q -sS`，不加载用户 curlrc，也不再使用 `-k`。可信成功、不可信 CA 与 hostname mismatch
   拒绝都经过真实本地 TLS/CONNECT 测试，不是只检查配置字符串。
3. **采集节点协议的 TLS**：不能由前两项推导。采集可能包含不同传输和节点自带策略；
   最终 EasyProxy 审计使用 `skip_cert_verify=False`，不声称所有输入节点都启用 TLS。

## 固定审计镜像与清理

`audit_image.py` 从准确根构建真实生产 tags 的 EasyProxy binary，使用固定官方 Python
linux/amd64 base manifest：

```text
docker.io/library/python@sha256:57d96f71de145a98e7ceca18ee4ade77bba4103a33a72dfaea026b23625c6bf0
```

最小 Dockerfile 只 COPY binary，不执行 apt/npm/Go 的浮动 Docker build。显式 image build
的 `--network none` 不代表禁止 builder 获取 FROM base。最后绑定实际 image ID、根 SHA、
仓库、binary SHA256、base、platform 和 `subscription-only-v1` scope；labels 是构建来源
核对，不是独立可信签名。该镜像不包含完整 UI、CFST、cloudflared 或 ECH connector。

消费阶段拒绝可变 tag、隐式 build/pull，实际容器 Image 与 operation/source labels 均核对。
严格模式启动前拒绝已有同名容器或旧 cleanup attempt。删除前取得匹配的精确 container ID，
单次 `rm --force <ID>` 必须正向确认后才生成成功 summary；promotion 要求相同 cleanup 证明。
O_EXCL/fsync attempt 文件让子进程/父进程不能在 UNKNOWN 后自动第二次发送 rm。

外层 Linux runner 在 timeout、非零退出或信号退出时终止整个普通子进程组，并检查 `/proc`
确认已无可运行组成员，再尝试本次精确容器的单次补清理。失败保留 writer。平台突然断电、
不可拦截的 executor kill、主动逃离进程组的程序不在绝对清理保证内；仍须只读 reconcile。
审计禁用不相关的地域路由及 GeoIP 自动下载，避免浮动网络输入。私密输出目录为 0700。

## 已保留的真实证据

路径：`linshi/easyproxy-sba-linux-20261026/`。

- `*-04.safe.json`、`go-04.jsonl`、`workerd-04.xml`：真实 Linux ECH、Pages/Functions、CLI
  构建；Go 80 tests/0 skip 和 vet；workerd 21 项 Node 统计通过（含 2 个父测试）、0 skip。
- `python-runtime-06.safe.json`：在准确官方 Python image 中运行产品 venv bootstrap，
  28 个 wheel 安装、实际 import 和 `pip check` 通过，不依赖 Windows pip bootstrap。
- `aggregator-runtime-10.safe.json`：两个固定工具在真实 Linux 执行，离线真实转换通过。
- `audit-image-08/image-provenance.json`：真实生产 tags binary 和准确本地 image ID。
- `audit-smoke-10.safe.json`：真实订阅消费、1 个稳定节点、租约探测、清理正向确认及独立
  查验容器不存在；本机 HTTP CONNECT fixture，不是外网代理质量或线上 ECH 验收。

失败夹具及失败 XML 保留，未通过放松 hash/TLS/identity 或重放云写入来转绿。最新全量
测试与安全检查的准确统计记录在 `integration-notes.md` 的最后续进段，不混称历史证据覆盖
之后新增的代码。完整联网采集、真实 R2 发布、正式 Actions 环境/调度和五动作仍待完成。
