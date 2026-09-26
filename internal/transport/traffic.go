package transport

import (
	"strings"
	"sync"
)

// TrafficCategory identifies application-level Edge<->SaaS traffic. The meter
// deliberately counts payload bytes handed to/read from net/http; it does not
// invent HTTP/TLS/TCP/IP overhead.
type TrafficCategory string

const (
	TrafficHeartbeat TrafficCategory = "heartbeat"
	TrafficControl   TrafficCategory = "control"
	TrafficDiscovery TrafficCategory = "discovery"
	TrafficFrames    TrafficCategory = "frames"
	TrafficEvents    TrafficCategory = "events"
	TrafficEvidence  TrafficCategory = "evidence"
	TrafficOTA       TrafficCategory = "ota"
	TrafficAnpr      TrafficCategory = "anpr"
)

// TrafficCounters is a cumulative application-payload snapshot.
type TrafficCounters struct {
	BytesSent     uint64 `json:"bytes_sent"`
	BytesReceived uint64 `json:"bytes_received"`
	Requests      uint64 `json:"requests"`
}

// TrafficSnapshot is keyed by stable traffic category.
type TrafficSnapshot map[TrafficCategory]TrafficCounters

// TrafficMeter is an optional, thread-safe accumulator. It stores counters
// only: never payloads, headers, device credentials or tokens.
type TrafficMeter struct {
	mu       sync.Mutex
	counters map[TrafficCategory]TrafficCounters
}

// NewTrafficMeter creates an empty meter.
func NewTrafficMeter() *TrafficMeter {
	return &TrafficMeter{counters: make(map[TrafficCategory]TrafficCounters)}
}

// Record adds one observation. Negative byte counts are ignored defensively.
func (m *TrafficMeter) Record(category TrafficCategory, sent, received int64, requests uint64) {
	if m == nil {
		return
	}
	if sent < 0 {
		sent = 0
	}
	if received < 0 {
		received = 0
	}
	m.mu.Lock()
	c := m.counters[category]
	c.BytesSent += uint64(sent)
	c.BytesReceived += uint64(received)
	c.Requests += requests
	m.counters[category] = c
	m.mu.Unlock()
}

// Snapshot returns an independent copy that callers may marshal or aggregate.
func (m *TrafficMeter) Snapshot() TrafficSnapshot {
	if m == nil {
		return TrafficSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(TrafficSnapshot, len(m.counters))
	for k, v := range m.counters {
		out[k] = v
	}
	return out
}

// Reset clears accumulated counters.
func (m *TrafficMeter) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.counters = make(map[TrafficCategory]TrafficCounters)
	m.mu.Unlock()
}

func classifyTrafficPath(path string) TrafficCategory {
	p := strings.ToLower(path)
	switch {
	case strings.Contains(p, "heartbeat"):
		return TrafficHeartbeat
	case strings.Contains(p, "discovery"):
		return TrafficDiscovery
	case strings.Contains(p, "frame"):
		return TrafficFrames
	case strings.Contains(p, "event"):
		return TrafficEvents
	case strings.Contains(p, "ota"):
		return TrafficOTA
	default:
		return TrafficControl
	}
}
