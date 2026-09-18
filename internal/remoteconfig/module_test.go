package remoteconfig

import (
	"context"
	"testing"
	"time"

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

func TestModule_PollApplyAckAndShutdown(t *testing.T) {
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
	m.SetPollInterval(10 * time.Millisecond)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	select {
	case status := <-acked:
		if status != string(ApplyStatusApplied) {
			t.Fatalf("acked status = %s, want %s", status, ApplyStatusApplied)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for poll/apply/ack")
	}

	select {
	case s := <-sink.statuses:
		if s.AppliedVersion != 1 {
			t.Fatalf("published status AppliedVersion = %d, want 1", s.AppliedVersion)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for health sink update")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestModule_NoContentIsNotAcked(t *testing.T) {
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
	m.SetPollInterval(10 * time.Millisecond)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if acks != 0 {
		t.Fatalf("acks = %d, want 0 (no config assigned)", acks)
	}
}
