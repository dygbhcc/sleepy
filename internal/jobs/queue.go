package jobs

// JobTypeRunPipeline is the only job type for now. The worker inspects the
// run's current status to decide which step to execute next.
const JobTypeRunPipeline = "RUN_PIPELINE"
