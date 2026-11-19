package conversation

import (
	"context"
	"fmt"
	"time"
)

// The stages a typed turn records.
const (
	// StagePrompt is reading what the prompt needs and rendering it.
	StagePrompt = "prompt"
	// StageModelFirstText runs from asking the model to its first text.
	StageModelFirstText = "model_first_text"
	// StageModelReply runs from asking the model to the end of its reply.
	StageModelReply = "model_reply"
	// StageSave is saving how the turn ended, and the words too for a turn cut
	// in on before the model was asked.
	StageSave = "save"
)

// Stage is one stage of a turn: when it started, how long it took, and what
// served it, such as a provider and model; empty for voice's own work.
type Stage struct {
	Name           string
	Implementation string
	Started        time.Time
	Took           time.Duration
	// Report is the model service's report on the reply, on the model stages
	// of a reply it finished.
	Report *ModelReport
}

// timingWrite bounds writing a turn's stage times, which happens once the
// caller has the whole reply. It is one insert, milliseconds on a healthy
// database, so 2s only stops a stalled one from holding the request open: a
// round guess.
const timingWrite = 2 * time.Second

// RecordTiming saves the stage times of a turn TakeTurn returned. Call it once
// the caller has the whole reply: the times are never on the reply's path, so
// a slow or failed write delays or fails nothing the user sees. The write runs
// even once the caller has gone, within its own short deadline.
func (s *Service) RecordTiming(ctx context.Context, t Turn) error {
	if len(t.Stages) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timingWrite)
	defer cancel()
	if err := s.turns.RecordStages(ctx, t); err != nil {
		return fmt.Errorf("record the turn's stage times: %w", err)
	}
	return nil
}

// Model names what answered the turn, as provider/model, from its reply's
// stage; empty for a turn the model was never asked about. Callers show it
// with the reply, so a fake model's fixed reply isn't taken for a broken agent.
func (t Turn) Model() string {
	for _, s := range t.Stages {
		if s.Name == StageModelReply {
			return s.Implementation
		}
	}
	return ""
}

// stagesOf lists a finished turn's stages. A reply that sent no text has no
// first-text stage. The model stages name what answered from the model
// service's report, or the model asked for when the reply ended without one.
func stagesOf(prompt, reply, save Stage, firstText time.Time, report *ModelReport, asked string) []Stage {
	served := asked
	if report != nil {
		served = report.Provider + "/" + report.Model
	}
	prompt.Name, save.Name = StagePrompt, StageSave
	reply.Name, reply.Implementation, reply.Report = StageModelReply, served, report
	stages := []Stage{prompt}
	if !firstText.IsZero() {
		stages = append(stages, Stage{Name: StageModelFirstText, Implementation: served, Started: reply.Started, Took: firstText.Sub(reply.Started), Report: report})
	}
	return append(stages, reply, save)
}
