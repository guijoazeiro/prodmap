package timeline

import "testing"

func TestDeploymentEventKindsAreClosedAndNonCausal(t *testing.T) {
	for status, want := range map[string]string{"pending": "deployment_pending", "running": "deployment_running", "succeeded": "deployment_succeeded", "failed": "deployment_failed", "cancelled": "deployment_cancelled", "rolled_back": "rollback_declared", "unexpected": "deployment_unknown"} {
		if got := DeploymentKind(status); got != want {
			t.Fatalf("status %q kind=%q want %q", status, got, want)
		}
	}
	if Priority("rollback_declared") != 10 || Priority("deployment_running") != 20 || Priority("runtime_observed") != 30 {
		t.Fatal("unexpected event priority")
	}
}
