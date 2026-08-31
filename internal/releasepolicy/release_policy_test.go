package releasepolicy

import (
	"os"
	"strings"
	"testing"
)

func loadPolicyFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read policy file %s: %v", path, err)
	}
	return string(b)
}

func requirePolicyFragments(t *testing.T, policy string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(policy, fragment) {
			t.Errorf("policy is missing required fragment %q", fragment)
		}
	}
}

func TestReleaseWorkflowPinsTaggedReleasesToMainAndAttestsArtifacts(t *testing.T) {
	workflow := loadPolicyFile(t, "../../.github/workflows/release.yml")
	ancestry := loadPolicyFile(t, "../../qa/verify-release-tag-ancestry.sh")

	// The workflow delegates ancestry verification to a fail-closed helper. Keep
	// both sides of that boundary under test so refactors cannot silently remove
	// the release-tag containment proof.
	requirePolicyFragments(t, workflow,
		"Require release tag commit to come from main",
		"bash qa/verify-release-tag-ancestry.sh \"${GITHUB_SHA}\"",
		"actions/attest-build-provenance@v2",
		"id-token: write",
		"attestations: write",
		"--sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 --numeric-owner",
		"sha256sum \"dist/${{ matrix.name }}.tar.gz\"",
	)
	requirePolicyFragments(t, ancestry,
		"git clone --quiet --no-tags --bare",
		"git -C \"$verify_repo/repo.git\" merge-base --is-ancestor \"$candidate_commit\" \"$main_commit\"",
		"Refusing release: candidate $candidate_commit is not contained in $main_ref ($main_commit).",
		"release_tag_ancestry=pass",
	)
}

func TestReleaseWorkflowKeepsUnsignedAndroidBuildOutOfPublishedAssets(t *testing.T) {
	workflow := loadPolicyFile(t, "../../.github/workflows/release.yml")
	validator := loadPolicyFile(t, "../../qa/release-asset-completeness.sh")

	// The publish job intentionally delegates asset-set and checksum enforcement
	// to one reusable fail-closed validator. Test both the delegation and the
	// validator contract so implementation can move out of YAML without losing
	// the release safety guarantees.
	requirePolicyFragments(t, workflow,
		"bondify-android-UNSIGNED-DEBUG.apk",
		"name: ci-android-unsigned-debug",
		"pattern: bondify-*",
		"Verify release asset completeness and checksums",
		"bash qa/release-asset-completeness.sh release-assets",
		"files: release-assets/*",
		"needs: [build, android-ci]",
	)
	requirePolicyFragments(t, validator,
		"refusing to publish Android APKs: signed Android release packaging is not enabled",
		"sha256sum -c",
		"SHA256SUMS",
		"missing checksum for",
		"orphan checksum without archive",
		"checksum filename mismatch",
		"release archive/checksum count mismatch",
		"release asset must be a regular non-symlink file",
	)
}
