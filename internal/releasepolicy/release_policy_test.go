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

	requirePolicyFragments(t, workflow,
		"bondify-android-UNSIGNED-DEBUG.apk",
		"name: ci-android-unsigned-debug",
		"pattern: bondify-*",
		"Refusing to publish an Android APK: no signed Android release configuration exists yet.",
		"sha256sum -c",
		"release-assets/SHA256SUMS",
		"needs: [build, android-ci]",
	)
}
