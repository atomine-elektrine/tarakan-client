package app

import (
	"strings"

	"github.com/atomine-elektrine/tarakan-client/internal/api"
	"github.com/atomine-elektrine/tarakan-client/internal/reviewdoc"
)

// isPickable reports whether a job from the queue can be worked (/report).
// Open and changes_requested are claimable. Active "claimed" rows are the
// caller's own claims (server only returns those); keep working them.
func isPickable(task api.Task) bool {
	switch task.Status {
	case "open", "changes_requested":
		return true
	case "claimed":
		// Active lease = ours (server filters). Inactive = expired, reclaimable.
		return true
	default:
		return false
	}
}

// isMyActiveClaim is true for jobs the queue returned as held by this client.
func isMyActiveClaim(task api.Task) bool {
	return task.Status == "claimed" && task.Lease != nil && task.Lease.Active
}

func taskMatchesOrigin(task api.Task, owner, name string) bool {
	if owner == "" || name == "" {
		return false
	}
	return strings.EqualFold(task.Repository.Owner, owner) &&
		strings.EqualFold(task.Repository.Name, name)
}

// pickReportJob chooses the next Job suitable for /report (finding kind).
// Only agent-capability jobs are safe to automate. Human and hybrid Jobs
// require participation the client cannot honestly claim in provenance.
func pickReportJob(tasks []api.Task) (api.Task, bool) {
	return pickReportJobPreferring(tasks, "", "", api.QueueFilter{})
}

// pickReportJobPreferring order:
//  1. Your active claims on the local repo
//  2. Your active claims anywhere
//  3. Open jobs on the local repo (agent > hybrid > human)
//  4. Open jobs anywhere
func pickReportJobPreferring(tasks []api.Task, localOwner, localName string, filter api.QueueFilter) (api.Task, bool) {
	var pickable []api.Task
	for _, task := range tasks {
		if !reviewdoc.FindingKinds[task.Kind] {
			continue
		}
		if task.Capability != "agent" {
			continue
		}
		if !isPickable(task) {
			continue
		}
		if !MatchesQueueFilter(task, filter) {
			continue
		}
		pickable = append(pickable, task)
	}
	if len(pickable) == 0 {
		return api.Task{}, false
	}

	first := func(pool []api.Task) (api.Task, bool) {
		if len(pool) > 0 {
			return pool[0], true
		}
		return api.Task{}, false
	}

	var myClaims, open []api.Task
	for _, task := range pickable {
		if isMyActiveClaim(task) {
			myClaims = append(myClaims, task)
		} else {
			open = append(open, task)
		}
	}

	// Finish what you already claimed first (local repo, then any).
	if localOwner != "" && localName != "" {
		for _, task := range myClaims {
			if taskMatchesOrigin(task, localOwner, localName) {
				return task, true
			}
		}
	}
	if len(myClaims) > 0 {
		return myClaims[0], true
	}

	if localOwner != "" && localName != "" {
		var local []api.Task
		for _, task := range open {
			if taskMatchesOrigin(task, localOwner, localName) {
				local = append(local, task)
			}
		}
		if task, ok := first(local); ok {
			return task, true
		}
	}
	return first(open)
}
