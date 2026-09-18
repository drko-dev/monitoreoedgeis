package remoteconfig

import (
	"context"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type fakeClient struct {
	getFunc func(ctx context.Context, deviceID, credential string) (*transport.RemoteConfig, error)
	ackFunc func(ctx context.Context, deviceID, credential string, version int64, status, errorCode string) error
}

func (f *fakeClient) GetDesiredConfig(ctx context.Context, deviceID, credential string) (*transport.RemoteConfig, error) {
	if f.getFunc != nil {
		return f.getFunc(ctx, deviceID, credential)
	}
	return nil, nil
}

func (f *fakeClient) AckRemoteConfig(ctx context.Context, deviceID, credential string, version int64, status, errorCode string) error {
	if f.ackFunc != nil {
		return f.ackFunc(ctx, deviceID, credential, version, status, errorCode)
	}
	return nil
}

type fakeHealthSink struct {
	statuses chan Status
}

func (f *fakeHealthSink) SetRemoteConfigStatus(s Status) {
	select {
	case f.statuses <- s:
	default:
	}
}

func TestModule_SyncOnce_AppliesAndAcks(t *testing.T) {
	acked := make(chan string, 1)
	client := &fakeClient{
		getFunc: func(ctx context.Context, deviceID, credential string) (*transport.RemoteConfig, error) {
			return &transport.RemoteConfig{Version: 1, Payload: []byte(`{"fps":10}`)}, nil
		},
		ackFunc: func(ctx context.Context, deviceID, credential string, version int64, status, errorCode string) error {
			acked <- status
			return nil
		},
	}

	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	engine := NewEngine(store, &fakeAdapter{})
	sink := &fakeHealthSink{statuses: make(chan Status, 4)}
	m := New(client, engine, "device-1", "cred-1", nil, WithHealthSink(sink))

	if err := m.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce() error = %v", err)
	}

	select {
	case status := <-acked:
		if status != string(ApplyStatusApplied) {
			t.Fatalf("acked status = %s, want %s", status, ApplyStatusApplied)
		}
	default:
		t.Fatal("SyncOnce did not ACK synchronously")
	}

	select {
	case s := <-sink.statuses:
		if s.AppliedVersion != 1 {
			t.Fatalf("published status AppliedVersion = %d, want 1", s.AppliedVersion)
		}
	default:
		t.Fatal("SyncOnce did not publish a health status synchronously")
	}

	if got := m.Status().AppliedVersion; got != 1 {
		t.Fatalf("m.Status().AppliedVersion = %d, want 1", got)
	}
}

func TestModule_SyncOnce_NoContentIsNotAcked(t *testing.T) {
	acks := 0
	client := &fakeClient{
		ackFunc: func(ctx context.Context, deviceID, credential string, version int64, status, errorCode string) error {
			acks++
			return nil
		},
	}
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	m := New(client, NewEngine(store, &fakeAdapter{}), "device-1", "cred-1", nil)

	if err := m.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce() error = %v", err)
	}
	if acks != 0 {
		t.Fatalf("acks = %d, want 0 (no config assigned)", acks)
	}
}

// SyncOnce must not ACK a status it could not durably record -- a
// persistence/unresolved-staging failure from Engine.ReceiveDesired
// propagates as an error instead.
func TestModule_SyncOnce_DoesNotAckOnEngineError(t *testing.T) {
	acks := 0
	client := &fakeClient{
		getFunc: func(ctx context.Context, deviceID, credential string) (*transport.RemoteConfig, error) {
			return &transport.RemoteConfig{Version: 1, Payload: []byte(`{"fps":10}`)}, nil
		},
		ackFunc: func(ctx context.Context, deviceID, credential string, version int64, status, errorCode string) error {
			acks++
			return nil
		},
	}
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	store.limitWrites = true
	store.writeBudget = 0 // fail every persistence attempt

	m := New(client, NewEngine(store, &fakeAdapter{}), "device-1", "cred-1", nil)
	if err := m.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected SyncOnce to return an error when the engine cannot persist")
	}
	if acks != 0 {
		t.Fatalf("acks = %d, want 0 (must not ACK an unrecorded status)", acks)
	}
}
