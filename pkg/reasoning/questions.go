package reasoning

import "context"

// Question is emitted by speculative read-only cognition when resolving the
// claim would likely improve future reasoning. Questions are intentionally not
// written back into the graph.
type Question struct {
	Claim        Claim   `json:"claim"`
	Rationale    string  `json:"rationale"`
	ExpectedGain float64 `json:"expected_gain"`
}

// QuestionSink receives bounded batches of questions from background cognition.
type QuestionSink interface {
	Emit(ctx context.Context, questions []Question) error
}
