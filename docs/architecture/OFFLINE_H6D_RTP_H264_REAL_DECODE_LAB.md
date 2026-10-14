# H6D — Valid RTP/H264 through real RTSP → processing → FFmpeg decoder (2026-10-14)

**Local laboratory PASS for RTSP decode; NOT full vision E2E, not runtime release-ready.**

New regression test `internal/processing/h6d_rtsp_real_h264_lab_test.go` uses the existing local `internal/rtsptest.Simulator`, actual `rtsp.Manager`, real `processing.Manager` and locally installed FFmpeg. An H264 Annex-B clip is generated using FFmpeg, split into NAL units, encapsulated as synthetic RTP packets and sent via RTSP interleaved TCP loopback. Unlike earlier simulator tests with dummy RTP content, packets are real decodable H264. The real supervisor reaches playing, creates the actual processing pipeline, and the FFmpeg decoder produces video frames. One observed run: **54 RTP packets, 31 frames decoded**. `go test ./internal/processing -run '^TestLabRTSPRealH264Decode$' -count=1 -v`: PASS; repeat `-count=2`: PASS.

The test checks that a positive number of frames are decoded; exact counts depend on timing and are not asserted. No actual hardware camera, external RTSP service, client footage, SaaS or production touched.

**Important scope:** the RTSP test does not invoke the YOLO Vision Worker. Separately, the real Go Vision Worker has detected a person in a frame decoded from a local file under lab IP-egress denial; this is **not** one connected RTSP → frame → YOLO → alert test. FFmpeg used here is still Homebrew dynamically-linked (17 external bin-level libraries detected); it is **not** hermetic, signed, pinned or fit for redistribution. Model distribution authorization remains false.

Next: integrate captured frames from real processing pipeline with real Worker in a single test, test fault/restart/network controls, source or build proper FFmpeg/FFprobe complete portable dependency closure and license audit, then H7B/H8. Keep `RUNTIME_READY=false` and `INFERENCE_READY=false`.
