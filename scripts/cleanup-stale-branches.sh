#!/bin/bash
# Bondify branch cleanup script
# Deletes all stale agent/* and automation/* branches
# Run from repo root: bash scripts/cleanup-stale-branches.sh

set -e

REPO="chewtoo22-rgb/Bondify"
STALE_BRANCHES=(
  # Agent branches
  "agent/ack-sack-retransmission"
  "agent/android-path-lifecycle"
  "agent/finish-bondify"
  "agent/phase7-stabilization"
  "agent/phase8-pairbond"
  "agent/phase8-pairbond-transport"
  "agent/phase8-pairbond-transport-work"
  "agent/repo-hygiene-audit"
  "agent/repo-hygiene-final"
  "agent/repo-hygiene-final2"
  "agent/repo-hygiene-final3"
  "agent/repo-hygiene-final4"
  "agent/repo-hygiene-final5"
  "agent/repo-hygiene-review"
  "agent/repo-hygiene-security"
  # Automation branches
  "automation/android-client-identity-contract"
  "automation/android-path-label-admission"
  "automation/android-runtime-selection-invariants"
  "automation/device-revoke-result-wire"
  "automation/diag-host-rebinding-guard"
  "automation/diagnostics-export-revalidate"
  "automation/enrollment-contract-current-main"
  "automation/enrollment-device-management-wire"
  "automation/enrollment-result-wire"
  "automation/enrollment-result-wire-main2"
  "automation/mobile-runtime-path-reservation"
  "automation/mobile-runtime-path-reservation-mainline"
  "automation/release-archive-contract-main"
  "automation/settings-parent-symlink-hardening"
  "automation/settings-pathpolicy-bridge"
)

echo "Deleting stale branches from $REPO..."
echo "Total branches to delete: ${#STALE_BRANCHES[@]}"
echo ""

for branch in "${STALE_BRANCHES[@]}"; do
  echo "Deleting $branch..."
  git push origin --delete "$branch" 2>/dev/null || echo "  ⚠️  Failed to delete $branch (may already be deleted)"
done

echo ""
echo "✅ Cleanup complete!"
echo "Remaining branches:"
git branch -r | grep -E "(agent|automation)/" | wc -l || echo "0 stale branches remaining"
