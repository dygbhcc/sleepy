package domain

// RunStatus represents the pipeline state of a run.
type RunStatus string

const (
	StatusPending     RunStatus = "PENDING"
	StatusScripted    RunStatus = "SCRIPTED"
	StatusVoiced      RunStatus = "VOICED"
	StatusThumbnailed RunStatus = "THUMBNAILED"
	StatusRendered    RunStatus = "RENDERED"
	StatusPackaged    RunStatus = "PACKAGED"
	StatusDone        RunStatus = "DONE"
	StatusFailed      RunStatus = "FAILED"
	StatusNeedsReview RunStatus = "NEEDS_REVIEW"
)

// JobStatus represents the state of a queued job.
type JobStatus string

const (
	JobPending JobStatus = "PENDING"
	JobRunning JobStatus = "RUNNING"
	JobDone    JobStatus = "DONE"
	JobFailed  JobStatus = "FAILED"
)

// NextStatus returns the status the run should advance to after the current
// step completes successfully.
func NextStatus(s RunStatus) RunStatus {
	switch s {
	case StatusPending:
		return StatusScripted
	case StatusScripted:
		return StatusVoiced
	case StatusVoiced:
		return StatusThumbnailed
	case StatusThumbnailed:
		return StatusRendered
	case StatusRendered:
		return StatusPackaged
	case StatusPackaged:
		return StatusDone
	default:
		return StatusFailed
	}
}
