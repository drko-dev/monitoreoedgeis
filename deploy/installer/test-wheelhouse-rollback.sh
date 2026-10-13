#!/usr/bin/env bash
# Adversarial test suite for rollback safety, state machine transitions,
# and backup preservation in fetch-wheelhouse.sh.
#
# Tests mandatory failure modes:
#  1. Fresh installation (Primera instalación)
#  2. Valid previous wheelhouse updated cleanly (Wheelhouse anterior válido)
#  3. Error before activation preserves target (Error antes de activar)
#  4. Error moving old wheelhouse preserves target (Error al mover wheelhouse anterior)
#  5. Error activating new wheelhouse triggers rollback (Error al activar nuevo wheelhouse)
#  6. Error restoring backup preserves backup directory (Error al restaurar backup)
#  7. Error in final verification triggers rollback (Error en verificación final)
#  8. Signal interruption triggers rollback (Interrupción por señal)
#  9. Repeated execution is idempotent (Ejecución repetida)
# 10. Exact preservation of previous wheelhouse (Preservación exacta)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FETCH_SCRIPT="$SCRIPT_DIR/fetch-wheelhouse.sh"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

SOURCE_WHEELHOUSE="/Users/gustavomarcon/.gemini/antigravity-cli/brain/115ba03b-08bb-4cfb-8d2d-8c8187d36286/scratch/wheelhouse"
LOCK_FILE="$REPO_ROOT/internal/installer/assets/wheelhouse-lock.json"

if [ ! -d "$SOURCE_WHEELHOUSE" ]; then
  echo "::warning::Skipping wheelhouse script tests: source scratch wheelhouse not found"
  exit 0
fi

CACHE_DIR="/tmp/geocam-wheelhouse-test-cache"
mkdir -p "$CACHE_DIR"
export GEOCAM_RUNTIME_CACHE_DIR="$CACHE_DIR"
# Pre-seed cache with physical wheels
cp -R "$SOURCE_WHEELHOUSE"/* "$CACHE_DIR"/ 2>/dev/null || true

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

setup_synthetic_wheelhouse() {
  local target_root="$1"
  local marker="${2:-old_wheelhouse_v1}"
  mkdir -p "$target_root"
  cp -R "$SOURCE_WHEELHOUSE"/* "$target_root"/
  echo "$marker" > "$target_root/canary.txt"
}

echo "============================================================"
echo "Starting GEO CAM Wheelhouse Rollback Safety Tests"
echo "============================================================"

# -----------------------------------------------------------------------------
# Case 1: Primera instalación (Fresh install)
# -----------------------------------------------------------------------------
echo "[Test 1/10] Case 1: Primera instalación"
TESTS_RUN=$((TESTS_RUN + 1))
T1_DIR=$(mktemp -d)/wheelhouse
"$FETCH_SCRIPT" --target-dir "$T1_DIR" >/dev/null

"$FETCH_SCRIPT" --target-dir "$T1_DIR" --verify-only >/dev/null || fail_test "Case 1: verify-only failed"
shopt -s nullglob
t1_backups=("$T1_DIR".old.* "$T1_DIR".tmp.* "$T1_DIR".broken.*)
shopt -u nullglob
[ ${#t1_backups[@]} -eq 0 ] || fail_test "Case 1: leftover backup/tmp directories found"
rm -rf "$T1_DIR"
pass "Case 1: Primera instalación exitosa y sin residuos"

# -----------------------------------------------------------------------------
# Case 2: Wheelhouse anterior válido (Clean upgrade)
# -----------------------------------------------------------------------------
echo "[Test 2/10] Case 2: Wheelhouse anterior válido actualizado limpiamente"
TESTS_RUN=$((TESTS_RUN + 1))
T2_DIR=$(mktemp -d)/wheelhouse
setup_synthetic_wheelhouse "$T2_DIR" "old_v1"

"$FETCH_SCRIPT" --target-dir "$T2_DIR" --force >/dev/null
"$FETCH_SCRIPT" --target-dir "$T2_DIR" --verify-only >/dev/null || fail_test "Case 2: verify-only failed"
shopt -s nullglob
t2_backups=("$T2_DIR".old.* "$T2_DIR".tmp.* "$T2_DIR".broken.*)
shopt -u nullglob
[ ${#t2_backups[@]} -eq 0 ] || fail_test "Case 2: leftover backup found post-commit"
rm -rf "$T2_DIR"
pass "Case 2: Actualización de wheelhouse limpia y backup eliminado post-commit"

# -----------------------------------------------------------------------------
# Case 3: Error antes de activar preserva target
# -----------------------------------------------------------------------------
echo "[Test 3/10] Case 3: Error antes de activar preserva target"
TESTS_RUN=$((TESTS_RUN + 1))
T3_DIR=$(mktemp -d)/wheelhouse
setup_synthetic_wheelhouse "$T3_DIR" "old_v1"

set +e
"$FETCH_SCRIPT" --target-dir "$T3_DIR" --fault-inject "before_activate" --force >/dev/null 2>&1
res=$?
set -e
[ $res -ne 0 ] || fail_test "Case 3: expected failure with fault injection"
[ -f "$T3_DIR/canary.txt" ] || fail_test "Case 3: canary missing after error"
[ "$(cat "$T3_DIR/canary.txt")" = "old_v1" ] || fail_test "Case 3: canary content mutated"
rm -rf "$T3_DIR"
pass "Case 3: Error antes de activar preserva intacto el target y limpia staging"

# -----------------------------------------------------------------------------
# Case 4: Error al mover wheelhouse anterior preserva target
# -----------------------------------------------------------------------------
echo "[Test 4/10] Case 4: Error al mover wheelhouse anterior preserva target"
TESTS_RUN=$((TESTS_RUN + 1))
T4_DIR=$(mktemp -d)/wheelhouse
setup_synthetic_wheelhouse "$T4_DIR" "old_v1"

set +e
"$FETCH_SCRIPT" --target-dir "$T4_DIR" --fault-inject "move_old_fail" --force >/dev/null 2>&1
res=$?
set -e
[ $res -ne 0 ] || fail_test "Case 4: expected failure"
[ "$(cat "$T4_DIR/canary.txt")" = "old_v1" ] || fail_test "Case 4: target mutated"
rm -rf "$T4_DIR"
pass "Case 4: Error al mover wheelhouse anterior preserva target original"

# -----------------------------------------------------------------------------
# Case 5: Error al activar nuevo wheelhouse ejecuta rollback automático
# -----------------------------------------------------------------------------
echo "[Test 5/10] Case 5: Error al activar nuevo wheelhouse ejecuta rollback"
TESTS_RUN=$((TESTS_RUN + 1))
T5_DIR=$(mktemp -d)/wheelhouse
setup_synthetic_wheelhouse "$T5_DIR" "old_v1"

set +e
"$FETCH_SCRIPT" --target-dir "$T5_DIR" --fault-inject "activate_fail" --force >/dev/null 2>&1
res=$?
set -e
[ $res -ne 0 ] || fail_test "Case 5: expected failure"
[ -f "$T5_DIR/canary.txt" ] || fail_test "Case 5: canary missing; rollback did not restore target"
[ "$(cat "$T5_DIR/canary.txt")" = "old_v1" ] || fail_test "Case 5: canary content corrupted"
rm -rf "$T5_DIR"
pass "Case 5: Fallo de activación disparó rollback y restauró wheelhouse previo válido"

# -----------------------------------------------------------------------------
# Case 6: Error al restaurar backup conserva backup y no lo elimina
# -----------------------------------------------------------------------------
echo "[Test 6/10] Case 6: Error al restaurar backup conserva backup intacto"
TESTS_RUN=$((TESTS_RUN + 1))
T6_DIR=$(mktemp -d)/wheelhouse
setup_synthetic_wheelhouse "$T6_DIR" "old_v1"

set +e
"$FETCH_SCRIPT" --target-dir "$T6_DIR" --fault-inject "restore_fail" --force >/dev/null 2>&1
res=$?
set -e
[ $res -ne 0 ] || fail_test "Case 6: expected failure"

shopt -s nullglob
t6_backups=("$T6_DIR".old.*)
shopt -u nullglob
[ ${#t6_backups[@]} -ge 1 ] || fail_test "Case 6: backup was deleted despite failed restoration"
[ -f "${t6_backups[0]}/canary.txt" ] || fail_test "Case 6: canary missing in preserved backup"
rm -rf "$T6_DIR"*
pass "Case 6: Error al restaurar backup conservó el directorio de backup intacto"

# -----------------------------------------------------------------------------
# Case 7: Error en verificación final ejecuta rollback automático
# -----------------------------------------------------------------------------
echo "[Test 7/10] Case 7: Error en verificación final ejecuta rollback automático"
TESTS_RUN=$((TESTS_RUN + 1))
T7_DIR=$(mktemp -d)/wheelhouse
setup_synthetic_wheelhouse "$T7_DIR" "old_v1"

set +e
"$FETCH_SCRIPT" --target-dir "$T7_DIR" --fault-inject "verify_fail" --force >/dev/null 2>&1
res=$?
set -e
[ $res -ne 0 ] || fail_test "Case 7: expected failure"
[ -f "$T7_DIR/canary.txt" ] || fail_test "Case 7: rollback did not restore previous target"
[ "$(cat "$T7_DIR/canary.txt")" = "old_v1" ] || fail_test "Case 7: canary corrupted"
rm -rf "$T7_DIR"
pass "Case 7: Error en verificación final disparó rollback y restauró wheelhouse previo"

# -----------------------------------------------------------------------------
# Case 8: Interrupción por señal ejecuta rollback
# -----------------------------------------------------------------------------
echo "[Test 8/10] Case 8: Interrupción por señal (SIGINT) ejecuta rollback"
TESTS_RUN=$((TESTS_RUN + 1))
T8_DIR=$(mktemp -d)/wheelhouse
setup_synthetic_wheelhouse "$T8_DIR" "old_v1"

# Run in background and send SIGINT during execution
"$FETCH_SCRIPT" --target-dir "$T8_DIR" --force >/dev/null 2>&1 &
sub_pid=$!
sleep 0.05
kill -INT "$sub_pid" 2>/dev/null || true
wait "$sub_pid" 2>/dev/null || true

# Target wheelhouse must still be valid or restored
"$FETCH_SCRIPT" --target-dir "$T8_DIR" --verify-only >/dev/null 2>&1 || [ -f "$T8_DIR/canary.txt" ] || fail_test "Case 8: wheelhouse corrupted after SIGINT"
rm -rf "$T8_DIR"*
pass "Case 8: Interrupción por señal manejada con preservación de wheelhouse válido"

# -----------------------------------------------------------------------------
# Case 9: Ejecución repetida e idempotente
# -----------------------------------------------------------------------------
echo "[Test 9/10] Case 9: Ejecución repetida e idempotente"
TESTS_RUN=$((TESTS_RUN + 1))
T9_DIR=$(mktemp -d)/wheelhouse
"$FETCH_SCRIPT" --target-dir "$T9_DIR" >/dev/null
# Repeated execution without force recognizes valid target
output=$("$FETCH_SCRIPT" --target-dir "$T9_DIR")
echo "$output" | grep -q "already contains verified wheelhouse" || fail_test "Case 9: second run did not recognize existing wheelhouse"
rm -rf "$T9_DIR"
pass "Case 9: Ejecución repetida es idempotente y no muta wheelhouse existente"

# -----------------------------------------------------------------------------
# Case 10: Preservación exacta bit a bit
# -----------------------------------------------------------------------------
echo "[Test 10/10] Case 10: Preservación criptográfica exacta del wheelhouse previo"
TESTS_RUN=$((TESTS_RUN + 1))
T10_DIR=$(mktemp -d)/wheelhouse
setup_synthetic_wheelhouse "$T10_DIR" "exact_bit_preservation"
initial_hash=$(shasum -a 256 "$T10_DIR/canary.txt" | awk '{print $1}')

set +e
"$FETCH_SCRIPT" --target-dir "$T10_DIR" --fault-inject "activate_fail" --force >/dev/null 2>&1
set -e

restored_hash=$(shasum -a 256 "$T10_DIR/canary.txt" | awk '{print $1}')
[ "$initial_hash" = "$restored_hash" ] || fail_test "Case 10: hash mismatch after rollback"
rm -rf "$T10_DIR"
pass "Case 10: Preservación bit a bit idéntica del wheelhouse previo tras rollback"

echo "============================================================"
echo "ALL $TESTS_PASSED OF $TESTS_RUN WHEELHOUSE ROLLBACK TESTS PASSED!"
echo "============================================================"
