package speech

import (
	"slices"
	"time"

	"github.com/mikaclements/voice-guide/internal/conversation"
)

// The stages speech times and saves with a turn, as the table of turn stages
// names them. The model's first text is the conversation module's own stage;
// speech only logs it beside the others.
const (
	stageFinalText        = "final_text"
	stageEndOfTurnCheck   = "end_of_turn_check"
	stageTaken            = "taken"
	stageFirstPhraseReady = "first_phrase_ready"
	stageFirstAudioSent   = "first_audio_sent"
)

// stageAt is a stage of a turn whose reply was spoken, and the moment it was
// reached; zero when it never was.
type stageAt struct {
	name string
	at   time.Time
}

// stagesAt lists, in the order they come, the moments the stages of a turn
// whose reply was spoken were reached, and the moment the user started waiting
// that they are counted from: the words arriving, or for a turn the user spoke
// the end of their speech. A turn the user spoke adds its final text, the
// end-of-turn check's answer and the turn taken. The log and the saved stages
// both read this list, so speech's stages can't disagree between them. The
// model's first text is counted here from the user waiting, as the log has it;
// the saved row of that name is conversation's, counted from the model being
// asked.
func stagesAt(received, firstText time.Time, audio played, said *heardTurn) (time.Time, []stageAt) {
	from, stages := received, []stageAt{{conversation.StageModelFirstText, firstText}, {stageFirstPhraseReady, audio.phraseReady}, {stageFirstAudioSent, audio.first}}
	if said != nil {
		from = said.ended
		stages = append([]stageAt{{stageFinalText, said.finalText}, {stageEndOfTurnCheck, said.checked}, {stageTaken, received}}, stages...)
	}
	return from, stages
}

// withSpeechStages returns turn with speech's stages added to the model's, to
// be saved with them in one write. The text-to-speech in use serves the audio
// stage; the rest is voice's own work.
func (s *Service) withSpeechStages(turn conversation.Turn, from time.Time, stages []stageAt) conversation.Turn {
	all := slices.Clone(turn.Stages)
	for _, st := range stages {
		if st.at.IsZero() || st.name == conversation.StageModelFirstText { // the latter is saved by conversation
			continue
		}
		stage := conversation.Stage{Name: st.name, Started: from, Took: max(st.at.Sub(from), 0)}
		if st.name == stageFirstAudioSent {
			stage.Implementation = s.settings.TextToSpeech
		}
		all = append(all, stage)
	}
	turn.Stages = all
	return turn
}

// logStages logs how long each stage of a turn whose reply was spoken took,
// counted from when the user started waiting, the model's first text too: the
// saved row of that name counts from the model being asked instead. A turn the user spoke also logs
// what ended it. A stage that didn't happen is left out.
func (s *Service) logStages(turn conversation.Turn, from time.Time, stages []stageAt, said *heardTurn) {
	attrs := []any{"session", turn.Session, "turn", turn.ID, "status", turn.Status, "model", turn.Model(), "text_to_speech", s.settings.TextToSpeech}
	if said != nil {
		attrs = append(attrs, "spoken", true, "turn_end", said.by, "talked_on", said.talkedOn)
	}
	for _, stage := range stages {
		if !stage.at.IsZero() {
			attrs = append(attrs, stage.name+"_ms", stage.at.Sub(from).Milliseconds())
		}
	}
	s.log.Info("turn spoken", attrs...)
}
