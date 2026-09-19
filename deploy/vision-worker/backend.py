"""Milestone K's inference backends.

InferenceBackend is the interface worker.py's socket server drives. Two
implementations:

- YOLOBackend: the real one (Ultralytics/PyTorch), CPU by default (K4).
  Ultralytics/torch are imported lazily, inside load(), so this module
  itself always imports cleanly even without them installed -- that keeps
  the socket-server/protocol code testable and the worker importable for
  static checks in an environment with no torch (this repo's own CI
  sandbox has none; see docs/deployment/appliance.md for the real-hardware
  gap this leaves).
- FakeBackend: a tiny deterministic stand-in used only by the unit tests
  (tests/test_worker.py) and never shipped as a runtime flag applicable to
  production -- there is no "--fake" switch for the real worker.py CLI to
  accidentally leave on, it is imported directly by tests instead.
"""

from __future__ import annotations

import time
from dataclasses import dataclass, field

# Fixed COCO class IDs GEO CAM already uses SaaS-side (geocam/detector.py) --
# K2 keeps this identical rather than inventing new class semantics.
CLASS_ID_PERSON = 0
VEHICLE_CLASS_IDS = {2: "car", 3: "motorcycle", 5: "bus", 7: "truck"}

DETECTION_TYPE_PERSON = "person"
DETECTION_TYPE_VEHICLE = "vehicle"


@dataclass
class Detection:
    class_id: int
    label: str
    type: str
    confidence: float
    bbox: list[float]  # [x1, y1, x2, y2]


@dataclass
class HealthResult:
    ready: bool
    # device is the EFFECTIVE device Ultralytics will be driven with, never the
    # raw configured string. device_requested is kept alongside it so an
    # operator (and Hito X's benchmark) can tell a resolved value from an echo.
    device: str = ""
    device_requested: str = ""
    models_loaded: list[str] = field(default_factory=list)
    error: str = ""
    # warning is a non-fatal, operator-visible note (for example a controlled
    # fallback from a requested accelerator that is not present on this host).
    warning: str = ""


@dataclass
class InferResult:
    inference_ms: float
    detections: list[Detection]


class InferenceBackend:
    def load(self) -> HealthResult:
        raise NotImplementedError

    def infer(self, jpeg_bytes: bytes, width: int, height: int) -> InferResult:
        raise NotImplementedError


def _cuda_available() -> bool:
    """Report whether this host's PyTorch can really use CUDA.

    Never raises: an unavailable, broken or absent torch means "no CUDA", which
    is the safe answer for device selection.
    """
    try:
        import torch  # lazy: torch arrives with ultralytics, not on its own
        return bool(torch.cuda.is_available())
    except Exception:  # noqa: BLE001 - absence of torch is not an error here
        return False


def resolve_device(requested: str) -> tuple[str, str]:
    """Resolve a configured device to one Ultralytics will actually accept.

    Returns (effective_device, warning). This exists because of a defect Hito X
    reproduced: `--device auto` — a value internal/fulledge.ParseDeviceMode
    documents and its own fallback path selects — was passed straight through to
    Ultralytics, which then raised `ValueError: Invalid CUDA 'device=auto'
    requested` on EVERY inference while the health handshake still reported
    ready=true. The agent's readiness gate therefore passed and 100% of
    inference failed at runtime. A requested `cuda` on a host without CUDA failed
    the same way, even though fulledge.HardwareManager documents a controlled
    fallback to CPU.

    This mirrors fulledge.HardwareManager.resolve()'s cpu/cuda/auto contract and
    adds nothing beyond it: no new backend, no new accelerator. Any other value
    (an explicit `mps`, a CUDA device index such as `0`, ...) is passed through
    untouched, because only the operator knows what this host has.
    """
    req = (requested or "").strip().lower()
    if req in ("", "auto"):
        return ("cuda" if _cuda_available() else "cpu"), ""
    if req == "cuda" and not _cuda_available():
        return "cpu", "cuda requested but this host's PyTorch reports no CUDA device; falling back to cpu"
    return req, ""


class YOLOBackend(InferenceBackend):
    """Real backend: yolo11s-pose.pt for persons, yolo11n.pt for vehicles
    (K2's fixed model choice -- never substituted). No model is fetched
    here: a missing weight file is a load error, not a download (K3).
    """

    def __init__(
        self,
        person_model_path: str,
        vehicle_model_path: str,
        device: str,
        imgsz: int,
        person_confidence: float,
        vehicle_confidence: float,
        nms_iou: float,
    ) -> None:
        self.person_model_path = person_model_path
        self.vehicle_model_path = vehicle_model_path
        self.device = device
        self.imgsz = imgsz
        self.person_confidence = person_confidence
        self.vehicle_confidence = vehicle_confidence
        self.nms_iou = nms_iou
        self._person_model = None
        self._vehicle_model = None

    def load(self) -> HealthResult:
        try:
            from ultralytics import YOLO  # lazy: see module docstring
        except ImportError as exc:
            return HealthResult(ready=False, error=f"ultralytics not installed: {exc}")

        requested = self.device
        effective, warning = resolve_device(requested)
        # Every later infer() call uses the resolved value, so the device the
        # handshake reports is the one the model is actually driven with.
        self.device = effective

        loaded: list[str] = []
        try:
            self._person_model = YOLO(self.person_model_path)
            loaded.append(self.person_model_path)
            self._vehicle_model = YOLO(self.vehicle_model_path)
            loaded.append(self.vehicle_model_path)
        except Exception as exc:  # noqa: BLE001 - report, never crash the worker
            return HealthResult(
                ready=False, device_requested=requested, models_loaded=loaded,
                error=str(exc), warning=warning,
            )

        return HealthResult(
            ready=True, device=effective, device_requested=requested,
            models_loaded=loaded, warning=warning,
        )

    def infer(self, jpeg_bytes: bytes, width: int, height: int) -> InferResult:
        import io

        from PIL import Image

        img = Image.open(io.BytesIO(jpeg_bytes)).convert("RGB")

        started = time.monotonic()
        detections: list[Detection] = []

        person_res = self._person_model.predict(
            img, device=self.device, imgsz=self.imgsz,
            conf=self.person_confidence, iou=self.nms_iou, verbose=False,
        )[0]
        detections.extend(_pose_person_detections(person_res, self.person_confidence))

        vehicle_res = self._vehicle_model.predict(
            img, device=self.device, imgsz=self.imgsz,
            conf=self.vehicle_confidence, iou=self.nms_iou, verbose=False,
            classes=list(VEHICLE_CLASS_IDS),
        )[0]
        detections.extend(_vehicle_detections(vehicle_res, self.vehicle_confidence))

        inference_ms = (time.monotonic() - started) * 1000.0
        return InferResult(inference_ms=inference_ms, detections=detections)


def _pose_person_detections(result, min_conf: float) -> list[Detection]:
    out: list[Detection] = []
    boxes = getattr(result, "boxes", None)
    if boxes is None:
        return out
    for box in boxes:
        cls_id = int(box.cls[0])
        conf = float(box.conf[0])
        if cls_id != CLASS_ID_PERSON or conf < min_conf:
            continue
        x1, y1, x2, y2 = [float(v) for v in box.xyxy[0]]
        out.append(Detection(
            class_id=cls_id, label="person", type=DETECTION_TYPE_PERSON,
            confidence=conf, bbox=[x1, y1, x2, y2],
        ))
    return out


def _vehicle_detections(result, min_conf: float) -> list[Detection]:
    out: list[Detection] = []
    boxes = getattr(result, "boxes", None)
    if boxes is None:
        return out
    for box in boxes:
        cls_id = int(box.cls[0])
        conf = float(box.conf[0])
        label = VEHICLE_CLASS_IDS.get(cls_id)
        if label is None or conf < min_conf:
            continue
        x1, y1, x2, y2 = [float(v) for v in box.xyxy[0]]
        out.append(Detection(
            class_id=cls_id, label=label, type=DETECTION_TYPE_VEHICLE,
            confidence=conf, bbox=[x1, y1, x2, y2],
        ))
    return out


class FakeBackend(InferenceBackend):
    """Deterministic backend for tests/test_worker.py only -- no torch, no
    real image decode. Never wired into worker.py's CLI.
    """

    def __init__(self, ready: bool = True, error: str = "") -> None:
        self._ready = ready
        self._error = error

    def load(self) -> HealthResult:
        if not self._ready:
            return HealthResult(ready=False, error=self._error)
        return HealthResult(ready=True, device="cpu", models_loaded=["fake-person", "fake-vehicle"])

    def infer(self, jpeg_bytes: bytes, width: int, height: int) -> InferResult:
        return InferResult(
            inference_ms=1.0,
            detections=[Detection(
                class_id=CLASS_ID_PERSON, label="person", type=DETECTION_TYPE_PERSON,
                confidence=0.9, bbox=[1.0, 2.0, 3.0, 4.0],
            )],
        )
