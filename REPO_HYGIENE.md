# Repository Hygiene and Maintenance Tasks

## Issue #33: Default Branch Migration (COMPLETED)

**Problem:** Repository default branch was `claude/hydra-wan-bonding-wtape5` instead of `main`.

**Impact:**
- New clones/forks land on a stale feature branch
- CI workflows configured for `main` weren't being triggered by default
- Confusing UX for contributors

**Fix:** Change default branch to `main`
1. Go to: https://github.com/chewtoo22-rgb/Bondify/settings
2. Scroll to "Default branch" section
3. Click the dropdown currently showing `claude/hydra-wan-bonding-wtape5`
4. Select `main`
5. Click "Update" and confirm the warning

---

## Stale Branch Cleanup (RECOMMENDED)

The repo has 60+ abandoned branches from old Copilot tasks. These clutter the branch list and should be deleted:

### Agent task branches (15 branches):
All starting with `agent/` and appear to be old task exploration/iteration branches:
- `agent/ack-sack-retransmission`
- `agent/android-path-lifecycle`
- `agent/finish-bondify`
- `agent/phase7-stabilization`
- `agent/phase8-pairbond`
- `agent/phase8-pairbond-transport`
- `agent/phase8-pairbond-transport-work`
- `agent/repo-hygiene-*` (audit, final, final2-5, review, security)

### Automation branches (30+ branches):
All starting with `automation/` - appear to be contract/verification task branches:
- `automation/android-*`
- `automation/device-revoke-result-wire`
- `automation/diag-host-rebinding-guard`
- `automation/diagnostics-export-revalidate`
- `automation/enrollment-*`
- `automation/mobile-runtime-*`
- `automation/release-archive-contract-main`
- `automation/settings-*`

**None of these branches have PRs or recent activity.** They are safe to delete.

**How to delete in bulk:**
1. Go to: https://github.com/chewtoo22-rgb/Bondify/branches
2. Click the trash icon next to each branch (can do many at once)
3. Confirm deletions

---

## Issue #34: Branch Protection Rules (RECOMMENDED)

**Objective:** Protect `main` from accidental breakage.

**Setup:**
1. Go to: https://github.com/chewtoo22-rgb/Bondify/settings/branches
2. Click "Add rule"
3. Set "Branch name pattern" to: `main`
4. Enable the following:
   - �� **Require a pull request before merging** (1 approval)
   - ✅ **Require status checks to pass before merging**
     - Require: `build` (all matrix entries: linux/amd64, linux/arm64, windows/amd64, android/arm64, android/arm)
     - Require: `lint`
   - ✅ **Require branches to be up to date before merging**
   - ✅ **Dismiss stale pull request approvals when new commits are pushed**
   - (Optional) ✅ **Restrict who can push to matching branches** (only admins if desired)
5. Click "Create"

**Why this matters:**
- Prevents broken code from landing on main
- Ensures CI passes before merge
- Enforces code review discipline
- Reduces emergency hotfix debt

---

## Post-Cleanup Checklist

- [ ] Change default branch to `main` (Settings)
- [ ] Delete stale branches (Branch settings)
- [ ] Set up branch protection on `main` (Branch protection rules)
- [ ] Verify CI triggers on next push to `main`
- [ ] Close issues #33 and #34 once done
