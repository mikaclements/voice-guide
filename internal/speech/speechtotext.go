package speech

import "context"

// SpeechToText turns the user's speech into text while they speak. One stream serves an audio
// connection for as long as it lasts, so resuming after a pause costs no new connection; each
// utterance's texts come back marked with the utterance they belong to.
type SpeechToText interface {
	// Transcribe reads the user's audio from audio until audio is closed or ctx is cancelled, and
	// sends what it heard on the returned channel: each utterance's rough text each time it
	// grows, then its final text once the utterance has ended, or a failure. A failure ends every
	// utterance not yet answered with it, each answered once; the stream goes on with the next
	// utterance, as an implementation may start afresh after one. Utterances are numbered from 0 in the order
	// they start; one ended with no audio has an empty final text. The channel is closed once
	// audio is closed and every ended utterance has its final text or failure, or soon after
	// ctx is cancelled.
	Transcribe(ctx context.Context, audio <-chan UtteranceAudio) <-chan UtteranceText
}

// UtteranceAudio is the next piece of the open utterance: its samples, 16 kHz mono PCM, then
// whether it ends there, which asks for its final text, or is dropped, as a sound taken for speech that wasn't is.
type UtteranceAudio struct {
	Samples []int16
	End     bool
	Drop    bool
}

// UtteranceText is what speech-to-text heard of one utterance, or the failure that lost it.
type UtteranceText struct {
	Utterance int
	// Text is the rough text so far, or the final text when Final is set.
	Text  string
	Final bool
	Err   error
}
