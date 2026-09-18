# Milestone J (J10–J11) — Hybrid Bandwidth & Precision Benchmark

This document establishes the reproducible benchmark harness, acceptance criteria, and measurement baseline for:
- **J10:** Bandwidth control & upload volume comparison (Cloud baseline vs. Hybrid candidate transport).
- **J11:** Precision/coverage evaluation, compute footprint, and semantic-safe cost reference.

---

## 1. Hardware Status & Provenance

> [!IMPORTANT]
> **REAL CAMERA = BLOCKED / NOT_EXECUTED**
> This harness has no RTSP/ONVIF capture code path — it never opens or consumes a physical camera stream, regardless of environment configuration.
> `TAPO_ONVIF_IP` being set only means a camera is **CONFIGURED** in the environment; it is never treated as evidence of an executed physical-camera run.
> Per project rules, physical numbers are **NOT fabricated or substituted with synthetic data disguised as real**.

| Category | Status | Details |
|---|---|---|
| **REAL CAMERA** | **BLOCKED / NOT MEASURED** | No physical Tapo TC70 camera reachable in this sandbox, and this harness has no RTSP capture path even when `TAPO_ONVIF_IP` is set |
| **CONTROLLED REPLAY** | **ACTIVE** | Loopback HTTP benchmark over identical deterministic synthetic frame sequences (`noisyYUV420p`), with real byte-count measurement through `internal/cloudsink` |
| **SYNTHETIC SELECTIVITY** | **ACTIVE** | Candidate/non-candidate split is a fixed, deterministic pattern (`i%3==0`) — a transport-sensitivity stress pattern, not real motion/event detection |
| **INTEGRATED HYBRID** | **NOT IMPLEMENTED** | Wiring this benchmark to the real Hybrid J1-J5 motion/ROI candidate decisions (`internal/processing`, PR #19) is future work |

---

## 2. Methodology & Harness (Fase 2 & Fase 6)

The comparative benchmark runs via `internal/cameratest/hybrid_bandwidth_bench_test.go`:

```bash
go test -tags localbench ./internal/cameratest/ -run TestHybridBandwidthBenchmark -v
```

### Protocol:
1. An identical sequence of $N$ synthetic frames is pre-generated (CONTROLLED REPLAY).
2. **Pass 1 (Cloud Baseline):** All $N$ frames are routed through `CloudSink.Route`, encoding to JPEG and pushing via HTTP.
3. **Pass 2 (Synthetic Selectivity):** The exact same $N$ frames are evaluated with a fixed deterministic pattern (`i%3==0`, SYNTHETIC SELECTIVITY — not a real motion/event decision); only "candidate" frames are routed to `CloudSink.Route`.
4. Results are compared side-by-side using real byte counters from `CloudSink.Status()`.

This measures whether byte accounting and transport behave correctly under reduced upload volume. It does **not** measure the real Hybrid J1-J5 motion detector's candidate accuracy or its real-world bandwidth savings (INTEGRATED HYBRID, not implemented here).

### Core Formula:
$$\text{Bandwidth Reduction \%} = \left( 1 - \frac{\text{Hybrid Uploaded Bytes}}{\text{Cloud Baseline Uploaded Bytes}} \right) \times 100$$

---

## 3. Results Observed in Controlled Replay (SYNTHETIC SELECTIVITY)

Stream configuration: 640×360, JPEG quality 85, target 5.0 FPS, 50 frames.

> [!NOTE]
> The "Synthetic Selectivity Mode" column below reflects the fixed `i%3==0` pattern, not the real Hybrid J1-J5 candidate algorithm. The 66.00% reduction is a controlled, synthetic result derived deliberately from that 1/3 ratio — it validates byte/transport accounting, it is not evidence of real-world Hybrid bandwidth savings.

| Metric | Cloud Baseline | Synthetic Selectivity Mode | Delta / Reduction |
|---|---|---|---|
| Input Frames | 50 | 50 | — |
| Frames Uploaded | 50 | 17 | -66.00% |
| Candidate Ratio | 100.0% | 34.00% | -66.00% |
| JPEG Bytes Uploaded | 11,406,945 B | 3,877,869 B | **66.00% bandwidth reduction (synthetic)** |
| Avg Bytes / Frame | 228,139 B | 228,110 B | ~Equal |
| Server Cross-Check | 11,406,945 B (Verified) | 3,877,869 B (Verified) | Exact match |

*Note: Frame content is entropy-heavy pseudo-random noise. Real camera video / real Hybrid J1-J5 candidate behavior (INTEGRATED HYBRID, REAL CAMERA) is NOT MEASURED / BLOCKED in this benchmark — no claim is made about its bytes-per-frame or candidate ratio.*

---

## 4. Acceptance Criteria Proposals (Fase 7)

1. **Comparability Requirement:** Benchmark runs must process identical frame counts and stream configurations.
2. **Bandwidth Savings:** Hybrid mode must yield positive bandwidth reduction ($\text{Reduction} > 0\%$) on non-continuous scenes.
3. **Fail-Closed Principle:** If candidate classification fails or is disabled, the system defaults to candidate transmission (`is_candidate = True`) to prevent silent event dropping.
