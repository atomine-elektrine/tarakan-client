package app

import (
	"strings"

	"github.com/atomine-elektrine/tarakan-client/internal/api"
)

// MatchesQueueFilter reports whether a job satisfies stars/language/kind constraints.
// Empty filter fields are ignored. Server-side filtering is preferred; this is a
// client-side safety net when the job payload includes repository metadata.
func MatchesQueueFilter(task api.Task, filter api.QueueFilter) bool {
	if filter.MinStars > 0 && task.Repository.StarsCount > 0 && task.Repository.StarsCount < int64(filter.MinStars) {
		return false
	}
	if lang := strings.TrimSpace(filter.Language); lang != "" {
		if task.Repository.PrimaryLanguage == "" {
			// Unknown language on the job: keep it when the server already filtered.
			// If both sides are empty of language data, still allow.
		} else if !strings.EqualFold(task.Repository.PrimaryLanguage, lang) {
			return false
		}
	}
	if kind := strings.TrimSpace(filter.Kind); kind != "" && !strings.EqualFold(task.Kind, kind) {
		return false
	}
	return true
}

// MatchesRepositoryFilter applies stars/language constraints to a queue repository.
func MatchesRepositoryFilter(repo api.QueueRepository, filter api.QueueFilter) bool {
	if filter.MinStars > 0 && repo.StarsCount < int64(filter.MinStars) {
		return false
	}
	if lang := strings.TrimSpace(filter.Language); lang != "" {
		if repo.PrimaryLanguage == "" || !strings.EqualFold(repo.PrimaryLanguage, lang) {
			return false
		}
	}
	return true
}
