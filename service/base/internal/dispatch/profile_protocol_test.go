package dispatch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"easy_proxies/internal/profile"
)

type fakeProfileResolver struct {
	mu       sync.RWMutex
	shared   *profile.CompiledProfile
	deviceID string
	device   *profile.CompiledProfile
}

func newFakeProfileResolver(t *testing.T, deviceID string) *fakeProfileResolver {
	t.Helper()
	shared, err := profile.Compile("shared", profile.KindShared, 1, profile.Definition{
		SchemaVersion: 1,
		Enabled:       true,
		FinalPolicy:   "PROXY",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	device, err := profile.Compile("device:"+deviceID, profile.KindDevice, 1, profile.Definition{
		SchemaVersion: 1,
		Enabled:       true,
		FinalPolicy:   "PROXY",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeProfileResolver{shared: shared, deviceID: deviceID, device: device}
}

func (r *fakeProfileResolver) Credentials() profile.CredentialSnapshot {
	return profile.CredentialSnapshot{Username: "easyproxy", Password: "secret", Generation: 1}
}

func (r *fakeProfileResolver) Resolve(identity profile.RequestIdentity) profile.Resolution {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if identity.ExplicitDeviceID == r.deviceID {
		return profile.Resolution{
			DeviceID:        r.deviceID,
			Source:          profile.IdentityExplicit,
			ProfileID:       r.device.ID(),
			ProfileRevision: r.device.Revision(),
			Profile:         r.device,
		}
	}
	return profile.Resolution{
		Source:          profile.IdentitySharedFallback,
		ProfileID:       r.shared.ID(),
		ProfileRevision: r.shared.Revision(),
		Profile:         r.shared,
	}
}

func (r *fakeProfileResolver) Observe(profile.Resolution, netip.Addr, time.Time) {}

func (r *fakeProfileResolver) setDevice(device *profile.CompiledProfile) {
	r.mu.Lock()
	r.device = device
	r.mu.Unlock()
}

type registryProfileResolver struct {
	registry *profile.Registry
}

func (r *registryProfileResolver) Credentials() profile.CredentialSnapshot {
	return r.registry.Credentials()
}

func (r *registryProfileResolver) Resolve(identity profile.RequestIdentity) profile.Resolution {
	return r.registry.Resolve(identity)
}

func (r *registryProfileResolver) Observe(profile.Resolution, netip.Addr, time.Time) {}

type profileProtocolHarness struct {
	address  string
	target   string
	outbound *dispatchTestOutbound
	provider *dispatchTestPoolProvider
}

func newProfileTestServer(t *testing.T, resolver ProfileResolver) profileProtocolHarness {
	t.Helper()
	origin := newDispatchTestOrigin(t)
	outbound := &dispatchTestOutbound{tag: "profile-test"}
	provider := &dispatchTestPoolProvider{outbound: outbound}
	server := NewServer(Config{Listen: "127.0.0.1:0", LocalServer: true, Profiles: resolver}, provider, nil, nil)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)
	server.mu.RLock()
	address := server.ln.Addr().String()
	server.mu.RUnlock()
	return profileProtocolHarness{
		address:  address,
		target:   origin.Listener.Addr().String(),
		outbound: outbound,
		provider: provider,
	}
}

func TestProfileSelectionMatchesAcrossHTTPConnectAndSOCKS5(t *testing.T) {
	resolver := newFakeProfileResolver(t, "laptop")
	harness := newProfileTestServer(t, resolver)
	assertHTTPUsesProfile(t, harness, "easyproxy+dev=laptop", "secret", "device:laptop")
	assertCONNECTUsesProfile(t, harness, "easyproxy+dev=laptop", "secret", "device:laptop")
	assertSOCKSUsesProfile(t, harness, "easyproxy+dev=laptop", "secret", "device:laptop")
}

func TestDisabledProfileForcesDirectWithoutPoolLookup(t *testing.T) {
	resolver := newFakeProfileResolver(t, "laptop")
	disabled, err := profile.Compile("device:laptop", profile.KindDevice, 1, profile.Definition{
		SchemaVersion: 1,
		Enabled:       false,
		FinalPolicy:   "PROXY",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver.setDevice(disabled)

	harness := newProfileTestServer(t, resolver)
	conn := dialDispatchTestTCP(t, harness.address)
	defer conn.Close()
	headers := make(http.Header)
	headers.Set("Proxy-Authorization", proxyAuthorization("easyproxy+dev=laptop+nosplit", "secret"))
	headers.Set(headerStrategy, "session")
	headers.Set(headerSplit, "off")
	body := dispatchTestProxyGET(t, conn, bufio.NewReader(conn), "http://"+harness.target+"/nosplit/disabled", headers)
	if body != "/disabled" {
		t.Fatalf("disabled profile response body = %q", body)
	}
	if reads := harness.provider.readCount(); reads != 0 {
		t.Fatalf("disabled profile read pool provider %d times, want 0", reads)
	}
	if dials := harness.outbound.snapshotDials(); len(dials) != 0 {
		t.Fatalf("disabled profile used proxy outbound: %#v", dials)
	}
}

func TestDisabledProfileForcesDirectForSOCKSUsernameTokens(t *testing.T) {
	resolver := newFakeProfileResolver(t, "laptop")
	disabled, err := profile.Compile("device:laptop", profile.KindDevice, 1, profile.Definition{
		SchemaVersion: 1,
		Enabled:       false,
		FinalPolicy:   "PROXY",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver.setDevice(disabled)

	harness := newProfileTestServer(t, resolver)
	conn := openSOCKSTunnel(t, harness, "easyproxy+dev=laptop+nosplit+session", "secret")
	defer conn.Close()
	if body := dispatchTestOriginGET(t, conn, bufio.NewReader(conn), "http://"+harness.target+"/disabled-socks"); body != "/disabled-socks" {
		t.Fatalf("disabled SOCKS profile response body = %q", body)
	}
	if reads := harness.provider.readCount(); reads != 0 {
		t.Fatalf("disabled SOCKS profile read pool provider %d times, want 0", reads)
	}
}

func TestDirectProfileDoesNotReadPoolProvider(t *testing.T) {
	resolver := newFakeProfileResolver(t, "laptop")
	direct, err := profile.Compile("device:laptop", profile.KindDevice, 1, profile.Definition{
		SchemaVersion: 1,
		Enabled:       true,
		FinalPolicy:   "DIRECT",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver.setDevice(direct)

	harness := newProfileTestServer(t, resolver)
	conn := dialDispatchTestTCP(t, harness.address)
	headers := make(http.Header)
	headers.Set("Proxy-Authorization", proxyAuthorization("easyproxy+dev=laptop", "secret"))
	if body := dispatchTestProxyGET(t, conn, bufio.NewReader(conn), "http://"+harness.target+"/direct-profile", headers); body != "/direct-profile" {
		t.Fatalf("direct profile response body = %q", body)
	}
	if reads := harness.provider.readCount(); reads != 0 {
		t.Fatalf("direct profile read pool provider %d times, want 0", reads)
	}
}

func TestProxyProfileWithoutPoolReturns502(t *testing.T) {
	resolver := newFakeProfileResolver(t, "laptop")
	origin := newDispatchTestOrigin(t)
	server := NewServer(Config{Listen: "127.0.0.1:0", LocalServer: true, Profiles: resolver}, nil, nil, nil)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)
	server.mu.RLock()
	address := server.ln.Addr().String()
	server.mu.RUnlock()

	request, err := http.NewRequest(http.MethodGet, origin.URL+"/missing-pool", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Proxy-Authorization", proxyAuthorization("easyproxy+dev=laptop", "secret"))
	response := roundTripProxyRequest(t, address, request)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("proxy profile without pool status = %d, want 502", response.StatusCode)
	}
}

func TestProxyProfileWithoutPoolReturnsSOCKSGeneralFailure(t *testing.T) {
	resolver := newFakeProfileResolver(t, "laptop")
	origin := newDispatchTestOrigin(t)
	server := NewServer(Config{Listen: "127.0.0.1:0", LocalServer: true, Profiles: resolver}, nil, nil, nil)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)
	server.mu.RLock()
	address := server.ln.Addr().String()
	server.mu.RUnlock()

	conn := dialDispatchTestTCP(t, address)
	authenticateSOCKS(t, conn, "easyproxy+dev=laptop", "secret")
	reply := writeSOCKSConnect(t, conn, origin.Listener.Addr().String())
	if reply[1] != repGeneralFailure {
		t.Fatalf("SOCKS reply = %v, want general failure", reply)
	}
}

func TestHTTPKeepAliveReadsProfileRegistryPerRequest(t *testing.T) {
	resolver := newFakeProfileResolver(t, "laptop")
	harness := newProfileTestServer(t, resolver)
	conn := dialDispatchTestTCP(t, harness.address)
	reader := bufio.NewReader(conn)
	headers := make(http.Header)
	headers.Set("Proxy-Authorization", proxyAuthorization("easyproxy+dev=laptop", "secret"))
	if body := dispatchTestProxyGET(t, conn, reader, "http://"+harness.target+"/profile-one", headers); body != "/profile-one" {
		t.Fatalf("first profile response body = %q", body)
	}

	updated, err := profile.Compile("device:laptop", profile.KindDevice, 2, profile.Definition{
		SchemaVersion: 1,
		Enabled:       true,
		FinalPolicy:   "PROXY",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver.setDevice(updated)
	if body := dispatchTestProxyGET(t, conn, reader, "http://"+harness.target+"/profile-two", headers); body != "/profile-two" {
		t.Fatalf("second profile response body = %q", body)
	}

	dials := harness.outbound.snapshotDials()
	if len(dials) != 2 || dials[0].directive.ProfileRevision != 1 || dials[1].directive.ProfileRevision != 2 {
		t.Fatalf("keep-alive profile revisions = %#v, want [1 2]", dials)
	}
}

func TestUnknownExplicitDeviceUsesSharedWithoutIPFallback(t *testing.T) {
	shared, err := profile.Compile("shared", profile.KindShared, 1, profile.Definition{
		SchemaVersion: 1,
		Enabled:       true,
		FinalPolicy:   "PROXY",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := profile.Compile("device:mapped", profile.KindDevice, 1, profile.Definition{
		SchemaVersion: 1,
		Enabled:       false,
		FinalPolicy:   "PROXY",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &registryProfileResolver{registry: profile.NewRegistry(
		shared,
		map[string]*profile.CompiledProfile{"mapped": mapped},
		[]profile.IPMapping{{MappingID: "loopback", Prefix: netip.MustParsePrefix("127.0.0.0/8"), DeviceID: "mapped", Priority: 1}},
		profile.CredentialSnapshot{Username: "easyproxy", Password: "secret", Generation: 1},
		1,
	)}
	harness := newProfileTestServer(t, resolver)
	assertHTTPUsesProfile(t, harness, "easyproxy+dev=unknown", "secret", "shared")
}

func TestHTTPAuthenticationDistinguishesCredentialsFromDeviceSyntax(t *testing.T) {
	resolver := newFakeProfileResolver(t, "laptop")
	harness := newProfileTestServer(t, resolver)

	for _, tt := range []struct {
		name     string
		username string
		password string
		status   int
	}{
		{name: "bad credentials hide malformed device", username: "wrong+dev=bad device", password: "secret", status: http.StatusProxyAuthRequired},
		{name: "authenticated malformed device", username: "easyproxy+dev=bad device", password: "secret", status: http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, "http://"+harness.target+"/auth", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Proxy-Authorization", proxyAuthorization(tt.username, tt.password))
			response := roundTripProxyRequest(t, harness.address, request)
			defer response.Body.Close()
			if response.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, tt.status)
			}
		})
	}
}

func assertLastProfileDial(t *testing.T, harness profileProtocolHarness, profileID string) {
	t.Helper()
	dials := harness.outbound.snapshotDials()
	if len(dials) == 0 {
		t.Fatal("expected at least one pool outbound dial")
	}
	last := dials[len(dials)-1]
	if last.directive.ProfileID != profileID {
		t.Fatalf("last dial profile = %q, want %q; dials=%#v", last.directive.ProfileID, profileID, dials)
	}
}

func proxyAuthorization(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func assertHTTPUsesProfile(t *testing.T, harness profileProtocolHarness, username, password, profileID string) {
	t.Helper()
	conn := dialDispatchTestTCP(t, harness.address)
	defer conn.Close()
	headers := make(http.Header)
	headers.Set("Proxy-Authorization", proxyAuthorization(username, password))
	body := dispatchTestProxyGET(t, conn, bufio.NewReader(conn), "http://"+harness.target+"/http", headers)
	if body == "" {
		t.Fatal("empty HTTP proxy response")
	}
	assertLastProfileDial(t, harness, profileID)
}

func assertCONNECTUsesProfile(t *testing.T, harness profileProtocolHarness, username, password, profileID string) {
	t.Helper()
	conn := dialDispatchTestTCP(t, harness.address)
	defer conn.Close()
	_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n", harness.target, harness.target, proxyAuthorization(username, password))
	request := &http.Request{Method: http.MethodConnect}
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d", response.StatusCode)
	}
	assertLastProfileDial(t, harness, profileID)
}

func assertSOCKSUsesProfile(t *testing.T, harness profileProtocolHarness, username, password, profileID string) {
	t.Helper()
	conn := openSOCKSTunnel(t, harness, username, password)
	defer conn.Close()
	assertLastProfileDial(t, harness, profileID)
}

func openSOCKSTunnel(t *testing.T, harness profileProtocolHarness, username, password string) net.Conn {
	t.Helper()
	conn := dialDispatchTestTCP(t, harness.address)
	authenticateSOCKS(t, conn, username, password)
	reply := writeSOCKSConnect(t, conn, harness.target)
	if reply[1] != repSuccess {
		t.Fatalf("reply=%v, want success", reply)
	}
	return conn
}

func authenticateSOCKS(t *testing.T, conn net.Conn, username, password string) {
	t.Helper()
	_, _ = conn.Write([]byte{0x05, 0x01, 0x02})
	method := make([]byte, 2)
	if _, err := io.ReadFull(conn, method); err != nil || !bytes.Equal(method, []byte{0x05, 0x02}) {
		t.Fatalf("method=%v err=%v", method, err)
	}
	auth := append([]byte{0x01, byte(len(username))}, []byte(username)...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, []byte(password)...)
	_, _ = conn.Write(auth)
	authReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, authReply); err != nil || authReply[1] != 0x00 {
		t.Fatalf("auth=%v err=%v", authReply, err)
	}
}

func writeSOCKSConnect(t *testing.T, conn net.Conn, target string) []byte {
	t.Helper()
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host).To4()
	request := append([]byte{0x05, 0x01, 0x00, 0x01}, ip...)
	request = append(request, byte(port>>8), byte(port))
	_, _ = conn.Write(request)
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read SOCKS reply: %v", err)
	}
	return reply
}

func roundTripProxyRequest(t *testing.T, address string, request *http.Request) *http.Response {
	t.Helper()
	conn := dialDispatchTestTCP(t, address)
	if err := request.WriteProxy(conn); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
