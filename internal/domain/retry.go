package domain

// RetryDecision is what to do after a failed gate run.
type RetryDecision int

// : "doğrulamada → retry" while the retry count is under
// budget, "retry → insan_kuyruğu" once it reaches budget. Budget is
// project-configured (gate.retry_budget, default 3); there is no 4th retry.
const (
	RetryAgain RetryDecision = iota
	RetryExhausted
)

// NextRetry decides whether to retry again or park the bead in the human
// queue, given how many retries have already been spent (0 before the first
// retry) against gate.retry_budget. A budget of 3 allows 3 retries after the
// initial attempt (4 gate evaluations total) before the bead is exhausted.
func NextRetry(retriesUsed, budget int) RetryDecision {
	if retriesUsed >= budget {
		return RetryExhausted
	}
	return RetryAgain
}
