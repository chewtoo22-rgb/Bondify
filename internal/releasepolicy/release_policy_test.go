package releasepolicy

import (
	"os"
	"strings"
	"testing"
)

func loadReleaseWorkflow(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	return string(b)
}

func requirePolicyFragments(t *testing.T, workflow string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(workflow, fragment) {
			t.Errorf("release workflow is missing required policy fragment %q", fragment)
		}
	}
}

func TestReleaseWorkflowPinsTaggedReleasesToMainAndAttestsArtifacts(t *testing.T) {
	workflow := loadReleaseWorkflow(t)

	requirePolicyFragments(t, workflow,
		"Require release tag commit to come from main",
		"git fetch --no-tags --depth=1 origin main",
		"git merge-base --is-ancestor \"$TAG_COMMIT\" origin/main",
		"actions/attest-build-provenance@v2",
		"id-token: write",
		"attestations: write",
		"--sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 --numeric-owner",
		"sha256sum \"dist/${{ matrix.name }}.tar.gz\"",
	)
}

func TestReleaseWorkflowKeepsUnsignedAndroidBuildOutOfPublishedAssets(t *testing.T) {
	workflow := loadReleaseWorkflow(t)

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
