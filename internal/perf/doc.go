// Package perf implements Hito X's reproducible VIDEO DECODE FPS and
// INFERENCE FPS measurement harness.
//
// # What this package is
//
// It is a measurement instrument, not a production component: nothing in
// cmd/geocam-edge or any internal/* production path imports it. It exists so
// the two throughput numbers Hito X asks for are produced by driving the
// REAL pipeline instead of a stand-in:
//
//   - Decode: a deterministic synthetic H.264 Annex-B clip (clip.go) is
//     packetized into RFC 6184 RTP (packetizer.go) and pushed through the
//     production chain — internal/rtsptest.Simulator (a real RTSP/RTP/AVP/TCP
//     server, the same fixture Hito W3 added) -> internal/rtsp.Manager ->
//     internal/processing.Manager -> the real `ffmpeg -f h264 -i pipe:0 -f
//     rawvideo pipe:1` subprocess -> real sampler -> real router
//     (decode.go). The counter values reported come from the pipeline's own
//     PipelineStatus, never from a parallel accounting invented here.
//
//   - Inference: the real Python vision worker (deploy/vision-worker) is
//     spawned and supervised by the production internal/vision.Worker, and
//     real decoded frames are pushed through the production
//     internal/vision.Sink (inference.go). The models are the ones this repo
//     documents (yolo11s-pose.pt, yolo11n.pt) — never substituted.
//
// # What is deliberately faked, and where
//
// Mocks exist only at two external boundaries, never on the unit under test:
//
//   - Clip generation and probing shell out to the host's `ffmpeg`/`ffprobe`.
//     Only the opt-in benchmarks need them; the untagged tests do not, which is
//     what lets CI validate the harness on a runner with no ffmpeg at all.
//     Those are external tools the Edge already requires at runtime (the
//     decoder IS an ffmpeg subprocess), so they are not mocks — but a
//     benchmark needs an encoder, and the shipped appliance image
//     deliberately has none. Generating the clip therefore needs a
//     full-featured ffmpeg on the machine running the benchmark; this is a
//     benchmark-host requirement, not a product requirement, and
//     docs/performance/VIDEO_DECODE_INFERENCE.md states it.
//
//   - DecodeOptions.DecoderFactory replaces the ffmpeg subprocess with a fake.
//     It exists so the harness's own counting/percentile/reporting logic is
//     testable without ffmpeg (and therefore runs in CI, which installs no
//     ffmpeg). Every run using it is labelled decoder_fake in its report —
//     a fake validates the harness, never the decoder's throughput.
//
// # Opt-in only
//
// Nothing here runs during `go test ./...`: the benchmark entry points live
// behind the `localbench` build tag AND require GEOCAM_PERF=1. The untagged
// tests in this package assert only that the harness compiles and that its
// counters/percentiles/wire format work — never a performance number.
package perf
