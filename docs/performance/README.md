# Edge camera performance benchmark

The Edge-side measurements use the existing Hito H pipeline and its real
`/status` metrics. The runnable physical-camera acceptance command is:

```bash
go test -tags integration ./internal/cameratest/... \
  -run TestIntegration_VideoPipeline -v -timeout 120s
```

The test requires `TAPO_ONVIF_USER` and `TAPO_ONVIF_PASS`, reads them only from
the environment, and skips safely when they are absent. It reports RTSP input,
completed access units, decoded frames, sampled frames, queue depth, dropped
frames, reconnects and decode latency.

The cross-repository report, controlled replay runner, SaaS worker
instrumentation and measured results live in the SaaS repository:
`monitoreoia/docs/performance/camera-worker-benchmark.md`.

Replay is explicitly not treated as an equivalent to physical cameras. RTP
packet-loss accounting and camera-side capture drops remain unmeasured until
sequence-aware packet instrumentation is added; existing packet and byte
counters are still reported without inventing a loss value.
