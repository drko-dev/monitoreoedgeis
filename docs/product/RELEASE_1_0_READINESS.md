# GEO CAM Edge — Release 1.0 Readiness (Hito Z6)

## Scope

This document separates three states that must not be conflated:

| State | Meaning |
| --- | --- |
| **SOFTWARE 1.0** | The integrated Edge code passes the final software gate and has no open functional blocker. |
| **RELEASE 1.0** | A real signed `v1.0.0` GitHub Release exists and its artifacts/signature have been produced by the tag workflow. |
| **DEPLOYED PROD** | A named production appliance/VM is actually running that released version. |

Hardware certification, real-camera validation, CUDA validation and the physical pilot are separate validation axes. Their absence constrains commercial/field claims, but does not silently reopen a software blocker that has been implemented and tested locally.

## Current Hito Z state

PR #100 completed the final integration gate **7/7 PASS** and merged unchanged
into `main @ 73b9f07fbab3a9a57f8bb652fdec9632a6cb38cc`.

The accepted Hito Z software blockers are:

| Blocker | Status | Evidence |
| --- | --- | --- |
| **B1 / G1** camera-target provisioning | **CLOSED — IMPLEMENTED / TESTED LOCAL** | discovery + credentials + ONVIF target builder feeds `rtsp.Manager.SetTargets`; real camera/DVR/NVR remain NOT_VALIDATED |
| **B2** Vision Worker packaging | **CLOSED** | worker sources ship in both release artifacts, install into the versioned release, script path derived, runtime prerequisite checked fail-closed |
| **B3** bounded Full Edge retention | **CLOSED** | events/captures/clips count/bytes/age bounds; F-A shared-capture protection; F-B durable-pending protection; clip free-disk gate |
| **B4** remote-config false applied | **CLOSED** | missing live runtime adapter fails closed via existing `rolled_back / APPLY_FAILED` contract |
| **B5** Full Edge status truthfulness | **CLOSED** | `full_edge` is exposed only when the effective profile is Full Edge |
| **B6** Python Vision Worker CI | **CLOSED** | `python-vision-worker` job runs the worker unittest suite on Python 3.12 without torch/ultralytics/model downloads |
| **B7** bandwidth defaults | **CLOSED BY EXPLICIT 1.0 DECISION** | byte/sec and FPS controls exist; no universal production Mbps/FPS value is invented |
| **B8** Hybrid saving claim | **CLOSED BY EXPLICIT 1.0 DECISION** | no quantitative saving claim is published without integrated/field measurement |
| **B9** physical validation | **SEPARATE / NOT_VALIDATED** | pilot + hardware certification not executed; this blocks field/commercial validation, not SOFTWARE 1.0 |
| **B10** release ffmpeg | **CLOSED** | release workflow builds static ffmpeg for amd64/arm64 and packaging is fail-closed if ffmpeg is absent |
| **B11** real tagged release | **NOT_VALIDATED** | no real signed `v1.0.0` release has been executed yet; this gates RELEASE 1.0, not SOFTWARE 1.0 |
| **B12** documentation drift | **CLOSED** | stale release/status documentation corrected during Hito Z integration |

## Software gate

The canonical PR gate in `.github/workflows/ci.yml` includes:

- gofmt
- `go vet ./...`
- `go test ./...`
- Go build
- linux/amd64 and linux/arm64 cross-build
- soak/perf smoke tests, including their race variants
- multi-arch release-artifact validation
- amd64 container smoke
- multi-arch Docker build
- `python-vision-worker` on Python 3.12
- `package-ffmpeg-fail-closed`

Hito Z additionally carries focal race/stress evidence for the concurrency-sensitive retention paths. A full repository-wide `go test -race ./...` is not part of the GitHub gate and must not be claimed otherwise.

## Software 1.0 decision

All identified functional blockers B1-B8 and B10 are closed, the final integration
gate passed 7/7, and PR #100 merged unchanged into `main`.

**SOFTWARE 1.0 = READY.**

This does not imply RELEASE 1.0 or DEPLOYED PROD, and it does not imply that
real cameras, a physical appliance, CUDA, a production WAN, the physical pilot,
or hardware certification have been validated.

## Release 1.0

The tag-driven workflow in `.github/workflows/release.yml`:

1. requires `OTA_SIGNING_KEY` and fails closed when it is absent;
2. builds static ffmpeg for linux/amd64 and linux/arm64;
3. packages the versioned appliance artifacts with ffmpeg required;
4. produces checksums;
5. signs `SHA256SUMS` with Ed25519;
6. publishes the GitHub Release artifacts.

**B11 remains NOT_VALIDATED until this workflow is actually executed for `v1.0.0`.**

A real release requires the signing secret to exist. The repository cannot prove the secret is configured merely by reading source.

## Deployed production

There is no deploy-to-production workflow in this repository. The only GitHub workflows are `ci.yml` and `release.yml`.

A real production deployment additionally requires an identified target (hostname/appliance/VM), architecture, installed/current version, provisioned OTA public key and an actual transport to that target. None of those target details are registered in the repository.

Until such a target is supplied and a deployment is executed:

**DEPLOYED PROD = NO / NO TARGET REGISTERED.**

## Validation boundaries carried into 1.0

The following remain explicitly **NOT_VALIDATED** and must not be converted into commercial claims:

- physical cameras and DVR/NVR behavior;
- physical 5–10 camera pilot;
- certified hardware;
- real ARM64 appliance runtime;
- real PyTorch/Ultralytics inference;
- CUDA/GPU runtime;
- real Hybrid bandwidth reduction;
- sustained production disk pressure/power-loss behavior;
- real SaaS + appliance field end-to-end operation.
