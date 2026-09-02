#!/usr/bin/env python3
"""Run an isolated dual-stack native-TUN data-plane validation with Docker."""

from __future__ import annotations

import argparse
from contextlib import nullcontext
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import textwrap
import time


ROOT = Path(__file__).resolve().parents[1]
SERVICE_ROOT = ROOT / "service" / "base"
CURRENT_STAGE = "startup"


def stage(name: str) -> None:
    global CURRENT_STAGE
    CURRENT_STAGE = name
    print(f"E2E_STAGE {name}", flush=True)


def run(*args: str, capture: bool = False, check: bool = True, cwd: Path | None = None) -> str:
    result = subprocess.run(
        list(args),
        cwd=cwd,
        check=False,
        text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )
    if check and result.returncode != 0:
        if capture:
            sys.stderr.write(result.stdout)
            sys.stderr.write(result.stderr)
        raise subprocess.CalledProcessError(result.returncode, args, output=result.stdout, stderr=result.stderr)
    return (result.stdout + result.stderr).strip() if capture else ""


def docker(*args: str, capture: bool = False, check: bool = True) -> str:
    return run("docker", *args, capture=capture, check=check)


def write_text(path: Path, value: str) -> None:
    path.write_text(textwrap.dedent(value).lstrip(), encoding="utf-8", newline="\n")


def build_quic_probe(work: Path) -> Path:
    source = work / "quic_probe.go"
    binary = work / "quic-probe"
    write_text(
        source,
        r'''
        package main

        import (
            "context"
            "crypto/rand"
            "crypto/rsa"
            "crypto/tls"
            "crypto/x509"
            "crypto/x509/pkix"
            "fmt"
            "io"
            "math/big"
            "net"
            "os"
            "time"

            quic "github.com/sagernet/quic-go"
        )

        const protocol = "easyproxy-quic-probe"

        func must(err error) {
            if err != nil { panic(err) }
        }

        func serverTLS() *tls.Config {
            key, err := rsa.GenerateKey(rand.Reader, 2048)
            must(err)
            template := x509.Certificate{
                SerialNumber: big.NewInt(1),
                Subject: pkix.Name{CommonName: "easyproxy-quic-probe"},
                NotBefore: time.Now().Add(-time.Hour),
                NotAfter: time.Now().Add(time.Hour),
                KeyUsage: x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
                ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
            }
            der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
            must(err)
            return &tls.Config{
                Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
                NextProtos: []string{protocol},
            }
        }

        func server() {
            listener, err := quic.ListenAddr("[::]:443", serverTLS(), &quic.Config{})
            must(err)
            defer listener.Close()
            must(os.WriteFile("/tmp/quic-ready", []byte("ready\n"), 0644))
            fmt.Println("QUIC_SERVER_READY")
            for index := 0; index < 2; index++ {
                conn, err := listener.Accept(context.Background())
                must(err)
                stream, err := conn.AcceptStream(context.Background())
                must(err)
                request, err := io.ReadAll(stream)
                must(err)
                _, err = stream.Write(append([]byte("QUIC-ECHO:"), request...))
                must(err)
                must(stream.Close())
                select {
                case <-conn.Context().Done():
                case <-time.After(3 * time.Second):
                }
            }
        }

        func client(host string) {
            ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
            defer cancel()
            conn, err := quic.DialAddr(ctx, net.JoinHostPort(host, "443"), &tls.Config{
                InsecureSkipVerify: true,
                NextProtos: []string{protocol},
            }, &quic.Config{})
            must(err)
            stream, err := conn.OpenStreamSync(ctx)
            must(err)
            _, err = stream.Write([]byte("hello"))
            must(err)
            must(stream.Close())
            response := make([]byte, len("QUIC-ECHO:hello"))
            _, err = io.ReadFull(stream, response)
            must(err)
            if string(response) != "QUIC-ECHO:hello" { panic(fmt.Sprintf("unexpected response %q", response)) }
            must(conn.CloseWithError(0, "done"))
            fmt.Println("PASS real-quic", host)
        }

        func main() {
            if len(os.Args) < 2 { panic("usage: server|client host...") }
            if os.Args[1] == "server" { server(); return }
            for _, host := range os.Args[2:] { client(host) }
        }
        ''',
    )
    env = os.environ.copy()
    env.update({"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64"})
    result = subprocess.run(
        ["go", "build", "-o", str(binary), str(source)],
        cwd=SERVICE_ROOT,
        env=env,
        check=False,
    )
    if result.returncode != 0:
        raise subprocess.CalledProcessError(result.returncode, result.args)
    return binary


def write_fixtures(work: Path) -> None:
    write_text(
        work / "config.yaml",
        """
        mode: pool
        log_level: debug
        database_path: /var/lib/easyproxy/data/data.db
        dns:
          enabled: true
          remote_servers: [https://cloudflare-dns.com/dns-query]
          strategy: as_is
        listener:
          address: 0.0.0.0
          port: 22323
          protocol: mixed
        management:
          enabled: true
          address: 127.0.0.1
          port: 29888
          password: e2e-validation-only
        subscription_refresh: {enabled: false}
        source_sync: {enabled: false}
        geoip: {enabled: false}
        routing:
          enabled: false
          default_strategy: stable
          use_default_rules: true
          final_policy: PROXY
        gateway:
          enabled: true
          mode: tun
          listen: 0.0.0.0:15001
          ingress:
            interfaces: [eth0]
            trusted_cidrs: [192.0.2.0/24, "2001:db8:1::/64"]
          capture: {tcp: disabled, udp: disabled, preserve_original_destination: true}
          routing: {final_policy: PROXY, no_available_proxy_policy: DIRECT}
          dns: {enabled: true, listen: "0.0.0.0:53"}
          tun:
            interface_name: easyproxy0
            addresses: [172.31.255.1/30, "fd31:255::1/126"]
            stack: mixed
            mtu: 1500
            ipv4: true
            ipv6: true
            udp: true
            strict_route: true
            dns_hijack: true
            fake_ip: true
            fake_ipv4_range: 198.18.0.0/16
            fake_ipv6_range: "fc00::/18"
        nodes: []
        subscriptions: []
        """,
    )
    write_text(
        work / "origin.py",
        r'''
        import socket, threading, time

        def tcp_server(family, address, ready):
            server = socket.socket(family, socket.SOCK_STREAM)
            if family == socket.AF_INET6:
                server.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
            server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            server.bind((address, 18080)); server.listen(16); ready.set()
            while True:
                client, _ = server.accept()
                try:
                    client.sendall(b"TCP-ECHO:" + client.recv(4096))
                finally:
                    client.close()

        def udp_server(family, address, ready):
            server = socket.socket(family, socket.SOCK_DGRAM)
            if family == socket.AF_INET6:
                server.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
            server.bind((address, 444)); ready.set()
            while True:
                data, peer = server.recvfrom(65535)
                server.sendto(b"UDP-ECHO:" + data, peer)

        ready_events = []
        for family, address in [(socket.AF_INET, "0.0.0.0"), (socket.AF_INET6, "::")]:
            for target in (tcp_server, udp_server):
                ready = threading.Event(); ready_events.append(ready)
                threading.Thread(target=target, args=(family, address, ready), daemon=True).start()
        if not all(ready.wait(5) for ready in ready_events): raise RuntimeError("origin listeners did not become ready")
        with open("/tmp/origin-ready", "w", encoding="utf-8") as marker: marker.write("ready\n")
        print("ORIGIN_READY", flush=True)
        while True: time.sleep(3600)
        ''',
    )
    write_text(
        work / "client.py",
        r'''
        import ipaddress, socket, struct, time

        def tcp(host, family):
            conn = socket.socket(family, socket.SOCK_STREAM); conn.settimeout(8)
            conn.connect((host, 18080)); conn.sendall(b"hello")
            response = conn.recv(1024); conn.close()
            assert response == b"TCP-ECHO:hello", response

        def udp(host, family):
            conn = socket.socket(family, socket.SOCK_DGRAM); conn.settimeout(8)
            conn.sendto(b"datagram", (host, 444)); response, _ = conn.recvfrom(1024); conn.close()
            assert response == b"UDP-ECHO:datagram", response

        def dns_query(qtype):
            qname = b"".join(bytes([len(part)]) + part.encode() for part in "example.com".split(".")) + b"\0"
            for attempt in range(10):
                txid = 0x4550 + attempt
                packet = struct.pack("!HHHHHH", txid, 0x0100, 1, 0, 0, 0) + qname + struct.pack("!HH", qtype, 1)
                conn = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); conn.settimeout(10)
                try: conn.sendto(packet, ("1.1.1.1", 53)); data, _ = conn.recvfrom(4096)
                except TimeoutError: conn.close(); continue
                conn.close()
                rid, flags, _, answers, _, _ = struct.unpack("!HHHHHH", data[:12])
                if rid != txid or not flags & 0x8000 or answers == 0:
                    time.sleep(0.5)
                    continue
                offset = 12
                while data[offset]: offset += 1 + data[offset]
                offset += 5
                for _ in range(answers):
                    if data[offset] & 0xC0 == 0xC0: offset += 2
                    else:
                        while data[offset]: offset += 1 + data[offset]
                        offset += 1
                    answer_type, _, _, length = struct.unpack("!HHIH", data[offset:offset + 10]); offset += 10
                    raw = data[offset:offset + length]; offset += length
                    if answer_type == qtype: return ipaddress.ip_address(raw)
            raise AssertionError("missing DNS answer")

        def fake_ip_http(address, family):
            conn = socket.socket(family, socket.SOCK_STREAM); conn.settimeout(12)
            conn.connect((str(address), 80))
            conn.sendall(b"GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n")
            response = conn.recv(256); conn.close()
            assert response.startswith(b"HTTP/1.1"), response

        tcp("203.0.113.2", socket.AF_INET); print("PASS ipv4-tcp")
        udp("203.0.113.2", socket.AF_INET); print("PASS ipv4-udp")
        tcp("2001:db8:2::2", socket.AF_INET6); print("PASS ipv6-tcp")
        udp("2001:db8:2::2", socket.AF_INET6); print("PASS ipv6-udp")
        ipv4_answer = dns_query(1); assert ipv4_answer in ipaddress.ip_network("198.18.0.0/16"); print("PASS dns-a", ipv4_answer)
        ipv6_answer = dns_query(28); assert ipv6_answer in ipaddress.ip_network("fc00::/18"); print("PASS dns-aaaa", ipv6_answer)
        fake_ip_http(ipv4_answer, socket.AF_INET); print("PASS fake-ipv4-connect")
        fake_ip_http(ipv6_answer, socket.AF_INET6); print("PASS fake-ipv6-connect")
        ''',
    )
    write_text(
        work / "socks_udp.py",
        r'''
        import socket, struct

        def receive(conn, length):
            result = b""
            while len(result) < length:
                part = conn.recv(length - len(result))
                if not part: raise RuntimeError("unexpected SOCKS EOF")
                result += part
            return result

        def receive_address(conn, address_type):
            if address_type == 1: host = socket.inet_ntop(socket.AF_INET, receive(conn, 4))
            elif address_type == 4: host = socket.inet_ntop(socket.AF_INET6, receive(conn, 16))
            elif address_type == 3: host = receive(conn, receive(conn, 1)[0]).decode()
            else: raise RuntimeError(f"unsupported ATYP {address_type}")
            return host, struct.unpack("!H", receive(conn, 2))[0]

        control = socket.create_connection(("192.0.2.2", 22323), 8); control.settimeout(8)
        control.sendall(b"\x05\x01\x00"); assert receive(control, 2) == b"\x05\x00"
        control.sendall(b"\x05\x03\x00\x01\x00\x00\x00\x00\x00\x00")
        header = receive(control, 4); assert header[:2] == b"\x05\x00", header
        relay_host, relay_port = receive_address(control, header[3])
        if relay_host in ("0.0.0.0", "::"): relay_host = "192.0.2.2"
        conn = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); conn.settimeout(10)
        request = b"\x00\x00\x00\x01" + socket.inet_aton("203.0.113.2") + struct.pack("!H", 444) + b"socks-udp"
        conn.sendto(request, (relay_host, relay_port)); response, _ = conn.recvfrom(4096)
        assert response[:3] == b"\x00\x00\x00" and response.endswith(b"UDP-ECHO:socks-udp"), response
        print("PASS socks5-udp-associate")
        ''',
    )

def copy_into(container: str, source: Path, target: str) -> None:
    docker("cp", str(source), f"{container}:{target}")

def wait_gateway(container: str) -> None:
    for _ in range(30):
        rules = docker("exec", container, "nft", "list", "table", "inet", "easyproxy_gateway", capture=True, check=False)
        if "table inet easyproxy_gateway" in rules:
            return
        status = docker("inspect", "-f", "{{.State.Running}}", container, capture=True, check=False)
        if status != "true":
            break
        time.sleep(1)
    docker("logs", "--tail", "150", container, check=False)
    raise RuntimeError("native TUN gateway did not become ready")

def wait_origin(container: str) -> None:
    for _ in range(30):
        ready = docker(
            "exec",
            container,
            "sh",
            "-c",
            "test -f /tmp/origin-ready && test -f /tmp/quic-ready && echo ready",
            capture=True,
            check=False,
        )
        if ready == "ready":
            return
        status = docker("inspect", "-f", "{{.State.Running}}", container, capture=True, check=False)
        if status != "true":
            break
        time.sleep(1)
    raise RuntimeError("origin TCP, UDP, and QUIC listeners did not become ready")

def wait_gateway_status(container: str) -> dict[str, object]:
    code = "import json,urllib.request; r=urllib.request.Request('http://127.0.0.1:29888/api/gateway/status',headers={'Authorization':'e2e-validation-only'}); print(json.dumps(json.load(urllib.request.urlopen(r))))"
    raw = ""
    for _ in range(30):
        raw = docker("exec", container, "python3", "-c", code, capture=True, check=False)
        try:
            status = json.loads(raw)
            if all(status.get(key) for key in ("enabled", "applied", "tun_ready")): return status
        except json.JSONDecodeError:
            pass
        time.sleep(1)
    state = docker("inspect", "-f", "{{.State.Status}}/{{.State.ExitCode}}", container, capture=True, check=False)
    raise RuntimeError(f"gateway management API did not become ready; container={state}; last={raw.splitlines()[-1:]}")

def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", default="easyproxy-native-tun:e2e")
    parser.add_argument("--build", action="store_true")
    args = parser.parse_args()
    if shutil.which("docker") is None or shutil.which("go") is None:
        raise RuntimeError("docker and go are required")
    stage("docker-capability")
    docker("info", capture=True)
    if args.build:
        stage("image-build")
        docker("build", "-f", str(ROOT / "deploy/service/base/Dockerfile"), "-t", args.image, str(ROOT))
    else:
        stage("image-inspect")
        docker("image", "inspect", args.image, capture=True)

    suffix = str(os.getpid())
    client_network = f"easyproxy-tun-client-{suffix}"
    origin_network = f"easyproxy-tun-origin-{suffix}"
    gateway = f"easyproxy-tun-gateway-{suffix}"
    origin = f"easyproxy-tun-origin-{suffix}"
    client = f"easyproxy-tun-client-{suffix}"
    containers = [client, gateway, origin]
    networks = [client_network, origin_network]
    work: Path | None = None
    try:
        with nullcontext(tempfile.mkdtemp(prefix="easyproxy-tun-e2e-")) as temp:
            work = Path(temp)
            (work / "data").mkdir()
            write_fixtures(work)
            stage("quic-probe-build")
            quic_probe = build_quic_probe(work)
            stage("network-create")
            docker("network", "create", "--ipv6", "--subnet", "192.0.2.0/24", "--gateway", "192.0.2.1", "--subnet", "2001:db8:1::/64", "--gateway", "2001:db8:1::1", client_network)
            docker("network", "create", "--ipv6", "--subnet", "203.0.113.0/24", "--gateway", "203.0.113.1", "--subnet", "2001:db8:2::/64", "--gateway", "2001:db8:2::1", origin_network)

            stage("origin-start")
            docker("run", "-d", "--name", origin, "--network", origin_network, "--ip", "203.0.113.2", "--ip6", "2001:db8:2::2", "--entrypoint", "sleep", args.image, "600")
            for fixture in ("origin.py",): copy_into(origin, work / fixture, f"/tmp/{fixture}")
            copy_into(origin, quic_probe, "/tmp/quic-probe")
            docker("exec", origin, "chmod", "755", "/tmp/quic-probe")
            docker("exec", "-d", origin, "python3", "/tmp/origin.py")
            docker("exec", "-d", origin, "/tmp/quic-probe", "server")
            stage("origin-ready")
            wait_origin(origin)

            stage("gateway-create")
            docker("create", "--name", gateway, "--network", client_network, "--ip", "192.0.2.2", "--ip6", "2001:db8:1::2", "--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW", "--device", "/dev/net/tun", "--sysctl", "net.ipv4.ip_forward=1", "--sysctl", "net.ipv6.conf.all.forwarding=1", "-e", "EASY_PROXY_RUN_AS_ROOT=1", "-v", f"{work / 'config.yaml'}:/etc/easyproxy/config.yaml:ro", "-v", f"{work / 'data'}:/var/lib/easyproxy/data", args.image)
            docker("network", "connect", "--ip", "203.0.113.3", "--ip6", "2001:db8:2::3", origin_network, gateway)
            docker("start", gateway)
            stage("gateway-ready")
            wait_gateway(gateway)

            stage("gateway-status")
            status = wait_gateway_status(gateway)
            expected = {"applied": True, "tun_ready": True, "ipv4": True, "ipv6": True, "udp": True, "dns": True}
            if any(status.get(key) != value for key, value in expected.items()):
                raise RuntimeError(f"unexpected gateway status: {status}")

            stage("client-start")
            docker("run", "-d", "--name", client, "--network", client_network, "--ip", "192.0.2.3", "--ip6", "2001:db8:1::3", "--cap-add", "NET_ADMIN", "--entrypoint", "sleep", args.image, "600")
            for fixture in ("client.py", "socks_udp.py"): copy_into(client, work / fixture, f"/tmp/{fixture}")
            copy_into(client, quic_probe, "/tmp/quic-probe")
            docker("exec", client, "chmod", "755", "/tmp/quic-probe")
            docker("exec", client, "sh", "-c", "ip route replace default via 192.0.2.2 && ip -6 route replace default via 2001:db8:1::2")
            stage("client-tcp-udp-dns-fakeip")
            docker("exec", client, "python3", "/tmp/client.py")
            stage("client-quic")
            docker("exec", client, "/tmp/quic-probe", "client", "203.0.113.2", "2001:db8:2::2")
            stage("client-socks-udp")
            docker("exec", client, "python3", "/tmp/socks_udp.py")
            stage("gateway-log-check")
            logs = docker("logs", gateway, capture=True)
            if "panic:" in logs:
                raise RuntimeError("gateway panicked during E2E validation")
            print("ALL_TUN_E2E_PASS")
            return 0
    finally:
        for container in containers:
            docker("rm", "-f", container, check=False)
        for network in networks:
            docker("network", "rm", network, check=False)
        if work is not None and work.exists():
            docker("run", "--rm", "--entrypoint", "chmod", "-v", f"{work}:/cleanup", args.image, "-R", "a+rwx", "/cleanup", capture=True, check=False)
            shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except subprocess.CalledProcessError as error:
        tail = ((error.stderr or "") + (error.output or "")).splitlines()[-1:]
        print(f"::error title=Native TUN E2E failed::{CURRENT_STAGE}: command exited with {error.returncode}; last={tail}", file=sys.stderr, flush=True)
        raise
    except Exception as error:
        detail = str(error).replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")
        print(f"::error title=Native TUN E2E failed::{CURRENT_STAGE}: {detail}", file=sys.stderr, flush=True)
        raise
