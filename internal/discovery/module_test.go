package discovery

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/wsdiscovery"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type mockTransportClient struct {
	mu           sync.Mutex
	claimFunc    func(ctx context.Context, deviceID, credential string) (*int, error)
	reportFunc   func(ctx context.Context, deviceID, credential string, runID int, req transport.DiscoveryReportRequest) error
	reportedReqs []transport.DiscoveryReportRequest
	reportedRuns []int
}

func (m *mockTransportClient) ClaimNextDiscoveryRun(ctx context.Context, deviceID, credential string) (*int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimFunc != nil {
		return m.claimFunc(ctx, deviceID, credential)
	}
	return nil, nil
}

func (m *mockTransportClient) ReportDiscoveryRun(ctx context.Context, deviceID, credential string, runID int, req transport.DiscoveryReportRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reportedRuns = append(m.reportedRuns, runID)
	m.reportedReqs = append(m.reportedReqs, req)
	if m.reportFunc != nil {
		return m.reportFunc(ctx, deviceID, credential, runID, req)
	}
	return nil
}

type fakePacketConn struct {
	mu      sync.Mutex
	packets [][]byte
	index   int
}

func (f *fakePacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	return len(p), nil
}

func (f *fakePacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.index >= len(f.packets) {
		return 0, nil, io.EOF
	}
	pkt := f.packets[f.index]
	f.index++
	copy(p, pkt)
	return len(pkt), &net.UDPAddr{IP: net.ParseIP("192.168.1.50"), Port: 3702}, nil
}

func (f *fakePacketConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (f *fakePacketConn) Close() error {
	return nil
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewModule_Validations(t *testing.T) {
	_, err := NewModule(ModuleOptions{})
	if err == nil {
		t.Fatal("expected error with nil engine")
	}

	engine := NewEngine(nil, nil, nil, nil, 2*time.Second, nil)
	mod, err := NewModule(ModuleOptions{
		Engine: engine,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mod.Name() != "discovery" {
		t.Errorf("expected discovery name, got %s", mod.Name())
	}
	if mod.Engine() != engine {
		t.Errorf("expected engine pointer match")
	}
	if mod.Status().State != StateIdle {
		t.Errorf("expected initial state idle, got %s", mod.Status().State)
	}
}

func TestCandidatePayloadFromDevice(t *testing.T) {
	dev := DiscoveredDevice{
		IP:           "192.168.1.150",
		Port:         8000,
		Path:         "/onvif/device_service",
		EPRAddress:   "urn:uuid:11223344-5566-7788-99aa-bbccddeeff00",
		Types:        "dn:NetworkVideoTransmitter",
		Scopes:       []string{"onvif://www.onvif.org/name/Cam1", "onvif://www.onvif.org/location/Gate"},
		Manufacturer: "VendorX",
		Model:        "ModelY",
		Serial:       "SN12345",
		Firmware:     "v1.0.0",
		DeviceType:   DeviceTypeCamera,
		AuthRequired: true,
	}

	payload := CandidatePayloadFromDevice(dev)
	if payload.Protocol != "onvif" {
		t.Errorf("expected onvif protocol, got %s", payload.Protocol)
	}
	if payload.EndpointHost != "192.168.1.150" || payload.EndpointPort != 8000 || payload.EndpointPath != "/onvif/device_service" {
		t.Errorf("endpoint mismatch: %+v", payload)
	}
	if payload.EPRAddress != dev.EPRAddress || payload.Types != dev.Types {
		t.Errorf("announcement mismatch: %+v", payload)
	}
	if payload.Manufacturer != "VendorX" || payload.Model != "ModelY" || payload.Serial != "SN12345" || payload.Firmware != "v1.0.0" {
		t.Errorf("device info mismatch: %+v", payload)
	}
	if payload.DeviceType != "camera" {
		t.Errorf("expected camera device_type, got %s", payload.DeviceType)
	}
	if !payload.AuthRequired {
		t.Errorf("expected AuthRequired true")
	}
}

func TestModuleLifecycle(t *testing.T) {
	inv := NewInventory()
	engine := NewEngine(nil, nil, inv, nil, 100*time.Millisecond, nil)

	mod, err := NewModule(ModuleOptions{
		Engine:       engine,
		Interval:     10 * time.Minute,
		PullInterval: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("failed to create module: %v", err)
	}

	ctx := context.Background()
	if err := mod.Start(ctx); err != nil {
		t.Fatalf("failed to start module: %v", err)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := mod.Stop(stopCtx); err != nil {
		t.Fatalf("failed to stop module: %v", err)
	}

	if mod.Status().State != StateStopped {
		t.Errorf("expected state stopped, got %s", mod.Status().State)
	}
}

func TestModule_ExecuteScanStatus(t *testing.T) {
	inv := NewInventory()
	mockConn := &fakePacketConn{
		packets: [][]byte{
			[]byte(`<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery">
  <soap:Body>
    <d:ProbeMatches>
      <d:ProbeMatch>
        <d:XAddrs>http://192.168.1.100:80/onvif/device_service</d:XAddrs>
      </d:ProbeMatch>
    </d:ProbeMatches>
  </soap:Body>
</soap:Envelope>`),
		},
	}
	scanner := wsdiscovery.NewScanner(func(localIP net.IP) (wsdiscovery.PacketConn, error) {
		return mockConn, nil
	})
	engine := NewEngine(scanner, nil, inv, nil, 100*time.Millisecond, nil)

	var lastStatus ModuleStatus
	mod, err := NewModule(ModuleOptions{
		Engine: engine,
		OnStatus: func(s ModuleStatus) {
			lastStatus = s
		},
	})
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}

	res, err := mod.executeScan(context.Background())
	if err != nil {
		t.Fatalf("executeScan: %v", err)
	}
	if len(res.DevicesFound) != 1 {
		t.Fatalf("expected 1 device, got %d", len(res.DevicesFound))
	}

	if lastStatus.State != StateIdle {
		t.Errorf("expected state idle after scan, got %s", lastStatus.State)
	}
	if lastStatus.DeviceCount != 1 {
		t.Errorf("expected device count 1, got %d", lastStatus.DeviceCount)
	}
	if lastStatus.LastSuccessAt.IsZero() {
		t.Errorf("expected non-zero LastSuccessAt")
	}
}

func TestModule_SaaSPullLoop_ClaimsAndReports(t *testing.T) {
	inv := NewInventory()
	mockConn := &fakePacketConn{
		packets: [][]byte{
			[]byte(`<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery">
  <soap:Body>
    <d:ProbeMatches>
      <d:ProbeMatch>
        <d:XAddrs>http://192.168.1.120:80/onvif/device_service</d:XAddrs>
      </d:ProbeMatch>
    </d:ProbeMatches>
  </soap:Body>
</soap:Envelope>`),
		},
	}
	scanner := wsdiscovery.NewScanner(func(localIP net.IP) (wsdiscovery.PacketConn, error) {
		return mockConn, nil
	})
	mockRoundTripper := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body>
    <tds:GetDeviceInformationResponse>
      <tds:Manufacturer>TestBrand</tds:Manufacturer>
      <tds:Model>CamPro</tds:Model>
      <tds:SerialNumber>SN999</tds:SerialNumber>
      <tds:FirmwareVersion>2.0.0</tds:FirmwareVersion>
    </tds:GetDeviceInformationResponse>
  </s:Body>
</s:Envelope>`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(body)),
			Header:     make(http.Header),
		}, nil
	})

	onvifClient := onvif.NewClient(time.Second, nil)
	onvifClient.SetTransport(mockRoundTripper)
	engine := NewEngine(scanner, onvifClient, inv, nil, 50*time.Millisecond, nil)

	claimed := false
	runID := 55
	mockTransport := &mockTransportClient{
		claimFunc: func(ctx context.Context, deviceID, credential string) (*int, error) {
			if !claimed {
				claimed = true
				return &runID, nil
			}
			return nil, nil
		},
	}

	mod, err := NewModule(ModuleOptions{
		Engine:       engine,
		Client:       mockTransport,
		DeviceID:     "dev-edge-1",
		Credential:   "secret-cred",
		Interval:     10 * time.Minute,
		PullInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}

	ctx := context.Background()
	if err := mod.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for report to arrive
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mockTransport.mu.Lock()
		count := len(mockTransport.reportedRuns)
		mockTransport.mu.Unlock()
		if count > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = mod.Stop(stopCtx)

	mockTransport.mu.Lock()
	defer mockTransport.mu.Unlock()

	if len(mockTransport.reportedRuns) == 0 {
		t.Fatal("expected at least 1 reported run")
	}
	if mockTransport.reportedRuns[0] != 55 {
		t.Errorf("expected run 55, got %d", mockTransport.reportedRuns[0])
	}
	req := mockTransport.reportedReqs[0]
	if req.Status != "completed" {
		t.Errorf("expected completed status, got %s", req.Status)
	}
	if len(req.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(req.Candidates))
	}
	cand := req.Candidates[0]
	if cand.EndpointHost != "192.168.1.120" || cand.Manufacturer != "TestBrand" || cand.Model != "CamPro" {
		t.Errorf("unexpected candidate data: %+v", cand)
	}
}

func TestModule_SaaSPullLoop_Unauthorized(t *testing.T) {
	inv := NewInventory()
	engine := NewEngine(nil, nil, inv, nil, 50*time.Millisecond, nil)

	mockTransport := &mockTransportClient{
		claimFunc: func(ctx context.Context, deviceID, credential string) (*int, error) {
			return nil, transport.ErrUnauthorized
		},
	}

	mod, err := NewModule(ModuleOptions{
		Engine:       engine,
		Client:       mockTransport,
		DeviceID:     "dev-edge-1",
		Credential:   "secret-cred",
		Interval:     10 * time.Minute,
		PullInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}

	if err := mod.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = mod.Stop(stopCtx)

	// Pull loop gracefully handled unauthorized without panic
}
