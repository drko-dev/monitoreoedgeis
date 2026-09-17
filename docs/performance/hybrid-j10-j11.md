# Milestone J (J10–J11) — Hybrid Bandwidth & Precision Benchmark

This document establishes the reproducible benchmark harness, acceptance criteria, and measurement baseline for:
- **J10:** Bandwidth control & upload volume comparison (Cloud baseline vs. Hybrid candidate transport).
- **J11:** Precision/coverage evaluation, compute footprint, and semantic-safe cost reference.

---

## 1. Hardware Status & Provenance

> [!IMPORTANT]
> **TC70 REAL CAMERA BENCHMARK = BLOCKED**
> In this sandbox environment, no physical Tapo TC70 camera or local RTSP credentials (`TAPO_ONVIF_*`, `GEOCAM_E2E_*`) are reachable.
> Per project rules, physical numbers are **NOT fabricated or substituted with synthetic data disguised as real**.

| Category | Status | Details |
|---|---|---|
| **Physical TC70 Camera** | **BLOCKED** | Environment lacks camera network or credentials |
| **Controlled Local Replay** | **ACTIVE** | Loopback HTTP benchmark over identical deterministic frame sequences |
| **Bandwidth Measurement** | **ACTIVE** | Direct measurement of JPEG byte body upload through `internal/cloudsink` |

---

## 2. Methodology & Harness (Fase 2 & Fase 6)

The comparative benchmark runs via `internal/cameratest/hybrid_bandwidth_bench_test.go`:

```bash
go test -tags localbench ./internal/cameratest/ -run TestHybridBandwidthBenchmark -v
```

### Protocol:
1. An identical sequence of $N$ frames is pre-generated.
2. **Pass 1 (Cloud Baseline):** All $N$ frames are routed through `CloudSink.Route`, encoding to JPEG and pushing via HTTP.
3. **Pass 2 (Hybrid Mode):** The exact same $N$ frames are evaluated locally; only candidate frames are routed to `CloudSink.Route`.
4. Results are compared side-by-side using real byte counters from `CloudSink.Status()`.

### Core Formula:
$$\text{Bandwidth Reduction \%} = \left( 1 - \frac{\text{Hybrid Uploaded Bytes}}{\text{Cloud Baseline Uploaded Bytes}} \right) \times 100$$

---

## 3. Results Observed in Local Replay

Stream configuration: 640×360, JPEG quality 85, target 5.0 FPS, 50 frames.

| Metric | Cloud Baseline | Hybrid Candidate Mode | Delta / Reduction |
|---|---|---|---|
| Input Frames | 50 | 50 | — |
| Frames Uploaded | 50 | 17 | -66.00% |
| Candidate Ratio | 100.0% | 34.00% | -66.00% |
| JPEG Bytes Uploaded | 11,406,945 B | 3,877,869 B | **66.00% bandwidth reduction** |
| Avg Bytes / Frame | 228,139 B | 228,110 B | ~Equal |
| Server Cross-Check | 11,406,945 B (Verified) | 3,877,869 B (Verified) | Exact match |

*Note: Frame content is entropy-heavy pseudo-random noise (upper bound). Real camera video with static backgrounds typically exhibits even lower bytes per frame and higher candidate suppression.*

---

## 4. Acceptance Criteria Proposals (Fase 7)

1. **Comparability Requirement:** Benchmark runs must process identical frame counts and stream configurations.
2. **Bandwidth Savings:** Hybrid mode must yield positive bandwidth reduction ($\text{Reduction} > 0\%$) on non-continuous scenes.
3. **Fail-Closed Principle:** If candidate classification fails or is disabled, the system defaults to candidate transmission (`is_candidate = True`) to prevent silent event dropping.
