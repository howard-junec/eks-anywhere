package e2e

import (
	"os"
	"strings"
	"testing"
)

func TestRufioCandidateImageMirrorSmoke(t *testing.T) {
	if os.Getenv("RUN_RUFIO_CANDIDATE_SMOKE") != "true" {
		t.Skip("set RUN_RUFIO_CANDIDATE_SMOKE=true to run the registry smoke test")
	}

	jobID := strings.TrimSpace(os.Getenv("CODEBUILD_BUILD_ID"))
	if jobID == "" {
		t.Fatal("CODEBUILD_BUILD_ID must be set")
	}

	candidateImage, err := prepareRufioCandidateImage(jobID + "-registry-smoke")
	if err != nil {
		t.Fatalf("preparing Rufio candidate image: %v", err)
	}
	session := &E2ESession{rufioCandidateImage: candidateImage}
	cleaned := false
	t.Cleanup(func() {
		if cleaned {
			return
		}
		if err := session.cleanupRufioCandidateImage(); err != nil {
			t.Errorf("cleaning up Rufio candidate image after test failure: %v", err)
		}
	})

	if candidateImage.digest == "" {
		t.Error("mirrored candidate image has no digest")
	}
	if len(candidateImage.runtimeDigests) == 0 {
		t.Error("mirrored candidate image has no runtime platform digests")
	}

	if err := session.cleanupRufioCandidateImage(); err != nil {
		t.Fatalf("cleaning up Rufio candidate image: %v", err)
	}
	if err := session.cleanupRufioCandidateImage(); err != nil {
		t.Fatalf("repeating Rufio candidate image cleanup: %v", err)
	}
	cleaned = true
}
