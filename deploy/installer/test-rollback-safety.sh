#!/usr/bin/env bash
# Comprehensive adversarial test suite for rollback safety, state machine transitions,
# and backup preservation in fetch-runtime-deps.sh.
#
# Tests all 12 mandatory failure modes:
#  1. Fresh installation (Primera instalación)
#  2. Valid previous runtime updated cleanly (Runtime anterior válido)
#  3. Error before activation preserves target (Error antes de activar)
#  4. Error moving old runtime preserves target (Error al mover runtime anterior)
#  5. Error activating new runtime triggers rollback (Error al activar nuevo runtime)
#  6. Error restoring backup preserves backup directory (Error al restaurar backup)
#  7. Error in final verification triggers rollback (Error en verificación final)
#  8. Interruption between renames triggers rollback (Interrupción entre renames / SIGINT)
#  9. Interruption during cleanup executes cleanly (Interrupción durante cleanup)
# 10. Repeated execution is idempotent (Ejecución repetida)
# 11. Exact bit-for-bit preservation of previous runtime (Preservación exacta)
# 12. Unrecovered backups are never deleted (Ausencia de eliminación de backups)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FETCH_SCRIPT="$SCRIPT_DIR/fetch-runtime-deps.sh"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

CACHE_DIR="/tmp/geocam-runtime-test-cache"
mkdir -p "$CACHE_DIR"
export GEOCAM_RUNTIME_CACHE_DIR="$CACHE_DIR"

# Global test counter
TESTS_RUN=0
TESTS_PASSED=0

pass() {
  echo "  ✓ PASS: $1"
  TESTS_PASSED=$((TESTS_PASSED + 1))
}

fail_test() {
  echo "  ✗ FAIL: $1" >&2
  exit 1
}

# Helper to create a synthetic valid Python distribution in a target directory
setup_synthetic_runtime() {
  local target_root="$1"
  local py_root="$target_root/python"
  local marker="${2:-initial_version}"

  mkdir -p "$py_root/bin" "$py_root/lib/python3.11/lib-dynload"

  # Create executable binary matching expected size and hash
  # For synthetic tests, we can copy the real binary from cache if extracted, or create a mock
  if [ -f "$CACHE_DIR/extracted_python/python/bin/python3.11" ]; then
    cp -R "$CACHE_DIR/extracted_python/python" "$target_root/"
  else
    # First-time seed: run fetch-runtime-deps.sh to populate cache and seed extracted structure
    mkdir -p "$CACHE_DIR/extracted_python"
    "$FETCH_SCRIPT" --target-dir "$CACHE_DIR/extracted_python" >/dev/null 2>&1
    cp -R "$CACHE_DIR/extracted_python/python" "$target_root/"
  fi

  # Add canary marker file for bit-for-bit preservation verification
  echo "$marker" > "$py_root/canary.txt"
}

echo "============================================================"
echo "Starting GEO CAM Rollback Safety Test Suite (12 Scenarios)"
echo "============================================================"

# Pre-populate cache so tests execute in seconds
mkdir -p "$CACHE_DIR/seed"
"$FETCH_SCRIPT" --target-dir "$CACHE_DIR/seed" >/dev/null 2>&1
rm -rf "$CACHE_DIR/seed"

# -----------------------------------------------------------------------------
# Case 1: Primera instalación (Fresh install)
# -----------------------------------------------------------------------------
echo "[Test 1/12] Case 1: Primera instalación"
TESTS_RUN=$((TESTS_RUN + 1))
T1_DIR=$(mktemp -d)
"$FETCH_SCRIPT" --target-dir "$T1_DIR" >/dev/null

[ -f "$T1_DIR/python/bin/python3.11" ] || fail_test "Case 1: python binary missing"
"$FETCH_SCRIPT" --target-dir "$T1_DIR" --verify-only >/dev/null || fail_test "Case 1: verify-only failed"
# Verify no temporary or backup directories exist
shopt -s nullglob
t1_backups=("$T1_DIR"/.python.old.* "$T1_DIR"/.python.tmp.* "$T1_DIR"/.python.broken.*)
shopt -u nullglob
[ ${#t1_backups[@]} -eq 0 ] || fail_test "Case 1: leftover backup/tmp directories found"
rm -rf "$T1_DIR"
pass "Case 1: Primera instalación exitosa y sin residuos"

# -----------------------------------------------------------------------------
# Case 2: Runtime anterior válido (Clean upgrade)
# -----------------------------------------------------------------------------
echo "[Test 2/12] Case 2: Runtime anterior válido actualizado limpiamente"
TESTS_RUN=$((TESTS_RUN + 1))
T2_DIR=$(mktemp -d)
setup_synthetic_runtime "$T2_DIR" "old_v1"

"$FETCH_SCRIPT" --target-dir "$T2_DIR" --force >/dev/null

"$FETCH_SCRIPT" --target-dir "$T2_DIR" --verify-only >/dev/null || fail_test "Case 2: verify-only failed"
[ ! -f "$T2_DIR/python/canary.txt" ] || fail_test "Case 2: old canary was not replaced"
shopt -s nullglob
t2_backups=("$T2_DIR"/.python.old.* "$T2_DIR"/.python.tmp.*)
shopt -u nullglob
[ ${#t2_backups[@]} -eq 0 ] || fail_test "Case 2: backup not deleted after successful commit"
rm -rf "$T2_DIR"
pass "Case 2: Actualización de runtime anterior limpia y backup eliminado post-commit"

# -----------------------------------------------------------------------------
# Case 3: Error antes de activar (Error in preparation/staging)
# -----------------------------------------------------------------------------
echo "[Test 3/12] Case 3: Error antes de activar preserva target"
TESTS_RUN=$((TESTS_RUN + 1))
T3_DIR=$(mktemp -d)
setup_synthetic_runtime "$T3_DIR" "canary_v3"

set +e
"$FETCH_SCRIPT" --target-dir "$T3_DIR" --force --fault-inject before_activate >/dev/null 2>&1
T3_EXIT=$?
set -e

[ "$T3_EXIT" -ne 0 ] || fail_test "Case 3: expected failure with fault-inject before_activate"
[ -f "$T3_DIR/python/canary.txt" ] || fail_test "Case 3: target runtime was destroyed!"
[ "$(cat "$T3_DIR/python/canary.txt")" = "canary_v3" ] || fail_test "Case 3: target canary modified"
shopt -s nullglob
t3_tmps=("$T3_DIR"/.python.tmp.*)
shopt -u nullglob
[ ${#t3_tmps[@]} -eq 0 ] || fail_test "Case 3: staging directory leaked"
rm -rf "$T3_DIR"
pass "Case 3: Error antes de activar preserva intacto el target y limpia staging"

# -----------------------------------------------------------------------------
# Case 4: Error al mover el runtime anterior (Failure during backup step)
# -----------------------------------------------------------------------------
echo "[Test 4/12] Case 4: Error al mover runtime anterior preserva target"
TESTS_RUN=$((TESTS_RUN + 1))
T4_DIR=$(mktemp -d)
setup_synthetic_runtime "$T4_DIR" "canary_v4"

set +e
"$FETCH_SCRIPT" --target-dir "$T4_DIR" --force --fault-inject move_old_fail >/dev/null 2>&1
T4_EXIT=$?
set -e

[ "$T4_EXIT" -ne 0 ] || fail_test "Case 4: expected failure with move_old_fail"
[ -f "$T4_DIR/python/canary.txt" ] || fail_test "Case 4: target was destroyed"
[ "$(cat "$T4_DIR/python/canary.txt")" = "canary_v4" ] || fail_test "Case 4: canary modified"
rm -rf "$T4_DIR"
pass "Case 4: Error al mover runtime anterior preserva target original"

# -----------------------------------------------------------------------------
# Case 5: Error al activar el runtime nuevo (Activation failure rollback)
# -----------------------------------------------------------------------------
echo "[Test 5/12] Case 5: Error al activar nuevo runtime ejecuta rollback automático"
TESTS_RUN=$((TESTS_RUN + 1))
T5_DIR=$(mktemp -d)
setup_synthetic_runtime "$T5_DIR" "canary_v5"

set +e
"$FETCH_SCRIPT" --target-dir "$T5_DIR" --force --fault-inject activate_fail >/dev/null 2>&1
T5_EXIT=$?
set -e

[ "$T5_EXIT" -ne 0 ] || fail_test "Case 5: expected failure with activate_fail"
[ -f "$T5_DIR/python/canary.txt" ] || fail_test "Case 5: rollback failed, target runtime not restored"
[ "$(cat "$T5_DIR/python/canary.txt")" = "canary_v5" ] || fail_test "Case 5: canary mismatch after rollback"
"$FETCH_SCRIPT" --target-dir "$T5_DIR" --verify-only >/dev/null || fail_test "Case 5: restored runtime invalid"
rm -rf "$T5_DIR"
pass "Case 5: Fallo de activación disparó rollback y restauró runtime previo válido"

# -----------------------------------------------------------------------------
# Case 6: Error al restaurar backup (Unrecovered backup safely preserved)
# -----------------------------------------------------------------------------
echo "[Test 6/12] Case 6: Error al restaurar backup conserva backup y no lo elimina"
TESTS_RUN=$((TESTS_RUN + 1))
T6_DIR=$(mktemp -d)
setup_synthetic_runtime "$T6_DIR" "canary_v6"

set +e
T6_OUTPUT=$("$FETCH_SCRIPT" --target-dir "$T6_DIR" --force --fault-inject restore_fail 2>&1)
T6_EXIT=$?
set -e

[ "$T6_EXIT" -ne 0 ] || fail_test "Case 6: expected failure with restore_fail"
echo "$T6_OUTPUT" | grep -q "CRITICAL: Backup restoration failed" || fail_test "Case 6: missing critical message"
echo "$T6_OUTPUT" | grep -q "Manual recovery command:" || fail_test "Case 6: missing manual recovery command"

# Assert that backup directory STILL exists on disk and contains canary
shopt -s nullglob
t6_backups=("$T6_DIR"/.python.old.*)
shopt -u nullglob
[ ${#t6_backups[@]} -ge 1 ] || fail_test "Case 6: CRITICAL BUG! Backup directory was deleted!"
[ -f "${t6_backups[0]}/canary.txt" ] || fail_test "Case 6: backup canary missing"
[ "$(cat "${t6_backups[0]}/canary.txt")" = "canary_v6" ] || fail_test "Case 6: backup canary corrupted"
rm -rf "$T6_DIR"
pass "Case 6: Error al restaurar backup conservó el directorio de backup intacto"

# -----------------------------------------------------------------------------
# Case 7: Error en verificación final (Post-activation verification failure)
# -----------------------------------------------------------------------------
echo "[Test 7/12] Case 7: Error en verificación final ejecuta rollback automático"
TESTS_RUN=$((TESTS_RUN + 1))
T7_DIR=$(mktemp -d)
setup_synthetic_runtime "$T7_DIR" "canary_v7"

set +e
"$FETCH_SCRIPT" --target-dir "$T7_DIR" --force --fault-inject verify_fail >/dev/null 2>&1
T7_EXIT=$?
set -e

[ "$T7_EXIT" -ne 0 ] || fail_test "Case 7: expected failure with verify_fail"
[ -f "$T7_DIR/python/canary.txt" ] || fail_test "Case 7: rollback failed to restore previous runtime"
[ "$(cat "$T7_DIR/python/canary.txt")" = "canary_v7" ] || fail_test "Case 7: canary corrupted"
"$FETCH_SCRIPT" --target-dir "$T7_DIR" --verify-only >/dev/null || fail_test "Case 7: restored runtime invalid"
rm -rf "$T7_DIR"
pass "Case 7: Error en verificación final disparó rollback y restauró runtime previo"

# -----------------------------------------------------------------------------
# Case 8: Interrupción entre renames (SIGINT between backup and activation)
# -----------------------------------------------------------------------------
echo "[Test 8/12] Case 8: Interrupción entre renames captura señal y ejecuta rollback"
TESTS_RUN=$((TESTS_RUN + 1))
T8_DIR=$(mktemp -d)
setup_synthetic_runtime "$T8_DIR" "canary_v8"

set +e
"$FETCH_SCRIPT" --target-dir "$T8_DIR" --force --fault-inject sigint_between_moves >/dev/null 2>&1
T8_EXIT=$?
set -e

# SIGINT should terminate with non-zero exit code
[ "$T8_EXIT" -ne 0 ] || fail_test "Case 8: expected non-zero exit from SIGINT"
[ -f "$T8_DIR/python/canary.txt" ] || fail_test "Case 8: trap failed to restore previous runtime after SIGINT"
[ "$(cat "$T8_DIR/python/canary.txt")" = "canary_v8" ] || fail_test "Case 8: canary corrupted after SIGINT rollback"
"$FETCH_SCRIPT" --target-dir "$T8_DIR" --verify-only >/dev/null || fail_test "Case 8: runtime corrupted after SIGINT"
rm -rf "$T8_DIR"
pass "Case 8: Interrupción entre renames (SIGINT) capturada y rollback ejecutado exitosamente"

# -----------------------------------------------------------------------------
# Case 9: Interrupción durante cleanup (Graceful single execution)
# -----------------------------------------------------------------------------
echo "[Test 9/12] Case 9: Interrupción durante cleanup ejecuta limpiamente"
TESTS_RUN=$((TESTS_RUN + 1))
T9_DIR=$(mktemp -d)
setup_synthetic_runtime "$T9_DIR" "canary_v9"

# Should succeed and handle cleanup hook without recursive crash
"$FETCH_SCRIPT" --target-dir "$T9_DIR" --force --fault-inject sigint_in_cleanup >/dev/null 2>&1 || fail_test "Case 9: failed during cleanup test"
"$FETCH_SCRIPT" --target-dir "$T9_DIR" --verify-only >/dev/null || fail_test "Case 9: verify-only failed"
rm -rf "$T9_DIR"
pass "Case 9: Trap handler previene reentrancia destructiva durante cleanup"

# -----------------------------------------------------------------------------
# Case 10: Ejecución repetida (Idempotence)
# -----------------------------------------------------------------------------
echo "[Test 10/12] Case 10: Ejecución repetida e idempotente"
TESTS_RUN=$((TESTS_RUN + 1))
T10_DIR=$(mktemp -d)
"$FETCH_SCRIPT" --target-dir "$T10_DIR" >/dev/null

# Second run without --force: must succeed immediately without re-downloading or re-moving
T10_OUT=$("$FETCH_SCRIPT" --target-dir "$T10_DIR")
echo "$T10_OUT" | grep -q "already contains verified Python" || fail_test "Case 10: expected already verified message"
"$FETCH_SCRIPT" --target-dir "$T10_DIR" --verify-only >/dev/null || fail_test "Case 10: verify failed"
rm -rf "$T10_DIR"
pass "Case 10: Ejecución repetida no muta runtime existente válido"

# -----------------------------------------------------------------------------
# Case 11: Preservación exacta del runtime anterior (Cryptographic canary check)
# -----------------------------------------------------------------------------
echo "[Test 11/12] Case 11: Preservación criptográfica exacta del runtime previo"
TESTS_RUN=$((TESTS_RUN + 1))
T11_DIR=$(mktemp -d)
setup_synthetic_runtime "$T11_DIR" "cryptographic_canary_v11"

# Compute hash of canary and dylib before failure
HASH_BEFORE_CANARY=$(shasum -a 256 "$T11_DIR/python/canary.txt" | awk '{print $1}')
HASH_BEFORE_DYLIB=$(shasum -a 256 "$T11_DIR/python/lib/libpython3.11.dylib" | awk '{print $1}')

set +e
"$FETCH_SCRIPT" --target-dir "$T11_DIR" --force --fault-inject activate_fail >/dev/null 2>&1
set -e

HASH_AFTER_CANARY=$(shasum -a 256 "$T11_DIR/python/canary.txt" | awk '{print $1}')
HASH_AFTER_DYLIB=$(shasum -a 256 "$T11_DIR/python/lib/libpython3.11.dylib" | awk '{print $1}')

[ "$HASH_BEFORE_CANARY" = "$HASH_AFTER_CANARY" ] || fail_test "Case 11: canary hash changed after rollback"
[ "$HASH_BEFORE_DYLIB" = "$HASH_AFTER_DYLIB" ] || fail_test "Case 11: dylib hash changed after rollback"
rm -rf "$T11_DIR"
pass "Case 11: Preservación bit a bit idéntica del runtime previo tras rollback"

# -----------------------------------------------------------------------------
# Case 12: Ausencia de eliminación de backups no recuperados (Unrecovered backup preservation)
# -----------------------------------------------------------------------------
echo "[Test 12/12] Case 12: Ausencia de eliminación de backups no recuperados"
TESTS_RUN=$((TESTS_RUN + 1))
T12_DIR=$(mktemp -d)
setup_synthetic_runtime "$T12_DIR" "unrecovered_canary_v12"

set +e
"$FETCH_SCRIPT" --target-dir "$T12_DIR" --force --fault-inject restore_fail >/dev/null 2>&1
set -e

# Target python was moved aside during activation failure, and restore failed
# The backup MUST NOT have been removed by EXIT trap!
shopt -s nullglob
t12_backups=("$T12_DIR"/.python.old.*)
shopt -u nullglob

[ ${#t12_backups[@]} -eq 1 ] || fail_test "Case 12: backup directory missing or pruned!"
[ -d "${t12_backups[0]}" ] || fail_test "Case 12: backup is not a directory"
[ -f "${t12_backups[0]}/bin/python3.11" ] || fail_test "Case 12: python binary missing in preserved backup"
[ -f "${t12_backups[0]}/canary.txt" ] || fail_test "Case 12: canary missing in preserved backup"
[ "$(cat "${t12_backups[0]}/canary.txt")" = "unrecovered_canary_v12" ] || fail_test "Case 12: canary altered"
rm -rf "$T12_DIR"
pass "Case 12: Backups no recuperados persisten intactos en disco tras salida del proceso"

echo "============================================================"
echo "ALL $TESTS_PASSED OF $TESTS_RUN ROLLBACK SAFETY TESTS PASSED SUCCESSFULLY!"
echo "============================================================"
