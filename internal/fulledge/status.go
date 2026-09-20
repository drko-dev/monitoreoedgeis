package fulledge

// Status contains the observability snapshot for Full Edge operations published on /status.
// It never exposes secrets, tokens, credentials, or image bytes.
type Status struct {
	LocalDetections        int64          `json:"local_detections"`
	LocalEventsCreated     int64          `json:"local_events_created"`
	EvidenceSaved          int64          `json:"evidence_saved"`
	EvidenceFailures       int64          `json:"evidence_failures"`
	LocalEventBacklog      int64          `json:"local_event_backlog"`
	CurrentInferenceDevice string         `json:"current_inference_device"`
	FallbackCPUCount       int64          `json:"fallback_cpu_count"`
	Hardware               HardwareStatus `json:"hardware"`
	Limits                 LimitsStatus   `json:"limits"`
	// Retention is the last completed Hito Z B3 sweep report. Zero-valued
	// (all fields 0, At the zero time) when retention is disabled or has not
	// swept yet — never omitted, so its absence is never mistaken for "no
	// growth happened".
	Retention RetentionReport `json:"retention"`
}

// HealthSink allows Service to publish its status snapshot into the agent health reporter.
type HealthSink interface {
	SetFullEdgeStatus(Status)
}
