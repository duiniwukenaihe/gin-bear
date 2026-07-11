//go:build windows

package releasee2e

import "testing"

func TestReleaseCandidateApplications(t *testing.T) {
	t.Skip("release application E2E requires Unix SIGTERM semantics")
}
