package app

import (
	"testing"

	"github.com/atomine-elektrine/tarakan-client/internal/api"
)

func TestPickReportJobPrefersAgentOpen(t *testing.T) {
	tasks := []api.Task{
		{ID: 1, Kind: "write_fix", Status: "open", Capability: "agent"},
		{ID: 2, Kind: "code_review", Status: "submitted", Capability: "agent"},
		{ID: 3, Kind: "code_review", Status: "open", Capability: "human"},
		{ID: 4, Kind: "threat_model", Status: "open", Capability: "agent"},
	}
	got, ok := pickReportJob(tasks)
	if !ok || got.ID != 4 {
		t.Fatalf("got %#v ok=%v, want agent finding job #4", got, ok)
	}
}

func TestPickReportJobNeverAutomatesHumanOrHybridWork(t *testing.T) {
	tasks := []api.Task{
		{ID: 1, Kind: "code_review", Capability: "human", Status: "open"},
		{ID: 2, Kind: "threat_model", Capability: "hybrid", Status: "open"},
	}
	if task, ok := pickReportJob(tasks); ok {
		t.Fatalf("picked non-agent job: %+v", task)
	}
}

func TestPickReportJobExpiredClaim(t *testing.T) {
	tasks := []api.Task{
		{ID: 9, Kind: "code_review", Status: "claimed", Capability: "agent", Lease: &api.Lease{Active: false}},
	}
	got, ok := pickReportJob(tasks)
	if !ok || got.ID != 9 {
		t.Fatalf("got %#v ok=%v, want expired claim #9", got, ok)
	}
}

func TestPickReportJobPrefersMyActiveClaim(t *testing.T) {
	tasks := []api.Task{
		{ID: 1, Kind: "code_review", Status: "open", Capability: "agent", Repository: api.Repository{Owner: "a", Name: "b"}},
		{ID: 2, Kind: "code_review", Status: "claimed", Capability: "agent", Lease: &api.Lease{Active: true}, Repository: api.Repository{Owner: "a", Name: "b"}},
	}
	got, ok := pickReportJobPreferring(tasks, "a", "b")
	if !ok || got.ID != 2 {
		t.Fatalf("got %#v ok=%v, want active claim #2 over open #1", got, ok)
	}
}

func TestPickReportJobNone(t *testing.T) {
	if _, ok := pickReportJob(nil); ok {
		t.Fatal("expected no pick")
	}
	if _, ok := pickReportJob([]api.Task{{ID: 1, Kind: "code_review", Status: "submitted"}}); ok {
		t.Fatal("submitted should not be claimable")
	}
}

func TestPickReportJobPreferringLocalOrigin(t *testing.T) {
	tasks := []api.Task{
		{ID: 1, Kind: "code_review", Status: "open", Capability: "agent", Repository: api.Repository{Owner: "other", Name: "repo"}},
		{ID: 2, Kind: "code_review", Status: "open", Capability: "agent", Repository: api.Repository{Owner: "acme", Name: "app"}},
	}
	got, ok := pickReportJobPreferring(tasks, "acme", "app")
	if !ok || got.ID != 2 {
		t.Fatalf("got %#v ok=%v, want local job #2", got, ok)
	}
	// No local match → take global preferred (agent first).
	got, ok = pickReportJobPreferring(tasks, "missing", "repo")
	if !ok || got.ID != 1 {
		t.Fatalf("got %#v ok=%v, want global agent job #1", got, ok)
	}
}
