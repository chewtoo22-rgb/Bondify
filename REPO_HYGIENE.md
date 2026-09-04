# Repository Hygiene and Maintenance Tasks

## Issue #33: Default Branch Migration

**Problem:** Repository default branch was `claude/hydra-wan-bonding-wtape5` instead of `main`.

**Fix steps:**
1. Go to: https://github.com/chewtoo22-rgb/Bondify/settings
2. Find "Default branch" section
3. Change from `claude/hydra-wan-bonding-wtape5` to `main`
4. Click Update and confirm

---

## Stale Branch Cleanup

Delete 60+ abandoned task branches cluttering the repo:

**Agent branches (15):** 
- agent/ack-sack-retransmission
- agent/android-path-lifecycle
- agent/finish-bondify
- agent/phase7-stabilization
- agent/phase8-pairbond
- agent/phase8-pairbond-transport
- agent/phase8-pairbond-transport-work
- agent/repo-hygiene-audit through agent/repo-hygiene-final5
- agent/repo-hygiene-review
- agent/repo-hygiene-security

**Automation branches (30+):** All starting with `automation/` 

None have PRs or recent commits. Safe to delete via: https://github.com/chewtoo22-rgb/Bondify/branches

---

## Issue #34: Branch Protection Rules

Protect `main` with CI requirements:

1. Go to: https://github.com/chewtoo22-rgb/Bondify/settings/branches
2. Add rule for `main` with:
   - ✅ Require PR before merging
   - ✅ Require status checks (build + lint)
   - ✅ Require up-to-date branches
3. Create
