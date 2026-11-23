package main

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mikaclements/voice-guide/internal/conversation"
	"github.com/mikaclements/voice-guide/internal/speech"
)

func env(overrides map[string]string) func(string) string {
	base := map[string]string{
		"DATABASE_URL":       "postgres://localhost/harbour",
		"VOICE_TICKET_KEY":   "0123456789abcdef0123456789abcdef",
		"VOICE_WORKSPACE_ID": "01a1de8b-bba7-7000-bad0-e92247a30f0f",
	}
	for k, v := range overrides {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestLoadConfigUsesDefaultsForTheOptionalSettings(t *testing.T) {
	cfg, err := loadConfig(env(nil))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.addr != "127.0.0.1:8080" {
		t.Errorf("default address = %q, want this machine only", cfg.addr)
	}
	if cfg.settings.UserContextChars != defaultContextLimit || cfg.settings.SessionContextChars != 280_000 {
		t.Errorf("default limits = %+v, want %d for user context and 280,000 for session context", cfg.settings, defaultContextLimit)
	}
}

func TestTheContextLimitCanBeSet(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"VOICE_USER_CONTEXT_MAX_CHARS": "5000", "VOICE_SESSION_CONTEXT_MAX_CHARS": "8000"}))
	if err != nil || cfg.settings.UserContextChars != 5000 || cfg.settings.SessionContextChars != 8000 {
		t.Errorf("loadConfig = %+v, %v; want 5000 and 8000", cfg.settings, err)
	}
}

func TestTheModelIsSonnetUnlessSet(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	set, err := loadConfig(env(map[string]string{"VOICE_MODEL": "claude-haiku-5-5"}))
	if unset.settings.Model != "claude-sonnet-5-5" || err != nil || set.settings.Model != "claude-haiku-5-5" {
		t.Errorf("model = %q unset, %q set (%v); want Sonnet 5.5, then Haiku 5.5", unset.settings.Model, set.settings.Model, err)
	}
}

func TestTheScreenInputLimitIs40000UnlessSet(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	set, err := loadConfig(env(map[string]string{"VOICE_SCREEN_INPUT_MAX_CHARS": "500"}))
	if unset.settings.ScreenInputChars != 40000 || err != nil || set.settings.ScreenInputChars != 500 {
		t.Errorf("limit = %d unset, %d set (%v); want 40000, then 500", unset.settings.ScreenInputChars, set.settings.ScreenInputChars, err)
	}
}

func TestTheCarriedLimitIs20000UnlessSet(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	set, err := loadConfig(env(map[string]string{"VOICE_CARRIED_MAX_CHARS": "900"}))
	if unset.settings.CarriedChars != 20000 || err != nil || set.settings.CarriedChars != 900 {
		t.Errorf("limit = %d unset, %d set (%v); want 20000, then 900", unset.settings.CarriedChars, set.settings.CarriedChars, err)
	}
}

func TestABoardAtTheDefaultLimitIsTakenAndFitsADataChannelMessageAndOneCharacterMoreIsRefused(t *testing.T) {
	cfg, err := loadConfig(env(nil))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	svc := conversation.NewService(nil, nil, nil, nil, nil, cfg.settings)
	board := func(chars int) []conversation.ScreenInputBlock {
		return []conversation.ScreenInputBlock{{Name: "board", Content: strings.Repeat("😀", chars)}}
	}
	if err := svc.CheckScreenInput(board(40_000)); err != nil {
		t.Errorf("a board of 40,000 characters: %v, want it taken", err)
	}
	if err := svc.CheckScreenInput(board(40_001)); !errors.Is(err, conversation.ErrScreenInputTooLarge) {
		t.Errorf("a board of 40,001 characters: %v, want it refused as over the limit", err)
	}
	// A turn at both limits, each character escaped at its longest, still fits one message.
	turn := `{"type":"turn","request_id":"01a1de8b-bba7-7000-bad0-e92247a30f0f","words":"` + strings.Repeat(`\ud83d\ude00`, defaultWordsLimit) +
		`","screen_input":[{"name":"board","content":"` + strings.Repeat(`\ud83d\ude00`, 40_000) + `"}]}`
	if limit := speech.MessageLimit(svc.TurnLimit()); len(turn) > limit {
		t.Errorf("a turn at both limits takes %d bytes, over the data channel's limit of %d", len(turn), limit)
	}
}

func TestTheLimitOnATurnsWordsIs4000UnlessSet(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	set, err := loadConfig(env(map[string]string{"VOICE_WORDS_MAX_CHARS": "500"}))
	if unset.settings.WordsChars != 4000 || err != nil || set.settings.WordsChars != 500 {
		t.Errorf("limit = %d unset, %d set (%v); want 4000, then 500", unset.settings.WordsChars, set.settings.WordsChars, err)
	}
}

func TestTheModelServiceIsOnThisMachineUnlessSet(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	set, err := loadConfig(env(map[string]string{"VOICE_MODEL_URL": "http://model:9000"}))
	if unset.modelURL != "http://127.0.0.1:8091" || err != nil || set.modelURL != "http://model:9000" {
		t.Errorf("model service = %q unset, %q set (%v); want http://127.0.0.1:8091, then http://model:9000", unset.modelURL, set.modelURL, err)
	}
}

func TestASessionEndsAfterFiveMinutesWithNoTurnUnlessSet(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	set, err := loadConfig(env(map[string]string{"VOICE_SESSION_IDLE": "10m"}))
	if unset.settings.SessionIdle != 5*time.Minute || err != nil || set.settings.SessionIdle != 10*time.Minute {
		t.Errorf("session idle = %v unset, %v set (%v); want 5m, then 10m", unset.settings.SessionIdle, set.settings.SessionIdle, err)
	}
}

func TestATurnRuns20SecondsAtMostUnlessSet(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	set, err := loadConfig(env(map[string]string{"VOICE_TURN_TIMEOUT": "90s"}))
	if unset.settings.TurnDeadline != 20*time.Second || err != nil || set.settings.TurnDeadline != 90*time.Second {
		t.Errorf("turn timeout = %v unset, %v set (%v); want 20s, then 90s", unset.settings.TurnDeadline, set.settings.TurnDeadline, err)
	}
}

func TestAudioTakesTheNamedAddressesWhenSet(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	set, err := loadConfig(env(map[string]string{"VOICE_AUDIO_IPS": "127.0.0.1, 192.168.1.20"}))
	if len(unset.audio.IPs) != 0 || err != nil || len(set.audio.IPs) != 2 || !set.audio.IPs[1].Equal(net.IPv4(192, 168, 1, 20)) {
		t.Errorf("audio addresses = %v unset, %v set (%v); want none (the transport's default), then the two named", unset.audio.IPs, set.audio.IPs, err)
	}
}

func TestAudioSharesOnePortAndAnnouncesOtherAddressesOnlyWhenSet(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	if unset.audio.Port != 0 || len(unset.audio.Announce) != 0 {
		t.Errorf("unset: port %d, announced %v; want none, so each connection takes its own ports on its own addresses", unset.audio.Port, unset.audio.Announce)
	}
	set, err := loadConfig(env(map[string]string{"VOICE_AUDIO_IPS": "172.18.0.4", "VOICE_AUDIO_UDP_PORT": "8443", "VOICE_AUDIO_ANNOUNCE_IPS": "127.0.0.1"}))
	if err != nil || set.audio.Port != 8443 || len(set.audio.Announce) != 1 || !set.audio.Announce[0].Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("set: port %d, announced %v (%v); want 8443 and 127.0.0.1", set.audio.Port, set.audio.Announce, err)
	}
}

func TestAServerHolds100AudioConnectionsUnlessSet(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	set, err := loadConfig(env(map[string]string{"VOICE_MAX_AUDIO_CONNECTIONS": "8"}))
	if unset.maxAudio != 100 || err != nil || set.maxAudio != 8 {
		t.Errorf("limit = %d unset, %d set (%v); want 100, then 8", unset.maxAudio, set.maxAudio, err)
	}
}

func TestRepliesAreSpokenByKokoroOnThisMachineInAfHeartUnlessSet(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	if unset.tts != "kokoro" || unset.ttsURL != "http://127.0.0.1:8880" || unset.voice != "af_heart" || unset.firstAudio != 5*time.Second {
		t.Errorf("text-to-speech = %q at %q in %q within %s unset; want kokoro at http://127.0.0.1:8880 in af_heart within 5s",
			unset.tts, unset.ttsURL, unset.voice, unset.firstAudio)
	}
	set, err := loadConfig(env(map[string]string{"VOICE_TTS": "fake", "VOICE_TTS_URL": "http://kokoro:8880", "VOICE_TTS_VOICE": "bf_emma", "VOICE_TTS_FIRST_AUDIO": "2s"}))
	if err != nil || set.tts != "fake" || set.ttsURL != "http://kokoro:8880" || set.voice != "bf_emma" || set.firstAudio != 2*time.Second {
		t.Errorf("text-to-speech = %q at %q in %q within %s set (%v); want each as set", set.tts, set.ttsURL, set.voice, set.firstAudio, err)
	}
}

func TestSessionsSpeakInTheServicesVoiceAtItsOwnPaceUnlessTheirStartOrAgentSaysOtherwise(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	set, _ := loadConfig(env(map[string]string{"VOICE_TTS_VOICE": "bf_emma"}))
	if unset.settings.Voice != "af_heart" || unset.settings.Speed != 1 || set.settings.Voice != "bf_emma" {
		t.Errorf("settings speak in %q at %v unset and %q set; want af_heart at 1, then bf_emma", unset.settings.Voice, unset.settings.Speed, set.settings.Voice)
	}
}

func TestAServiceVoiceThatIsNotAVoiceNameStopsTheStart(t *testing.T) {
	if _, err := loadConfig(env(map[string]string{"VOICE_TTS_VOICE": "../af_heart"})); err == nil || !strings.Contains(err.Error(), "VOICE_TTS_VOICE") {
		t.Errorf("loadConfig = %v, want an error naming VOICE_TTS_VOICE", err)
	}
}

func TestSpeechIsNoticedBySileroWithItsStartingValuesUnlessSet(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	want := speech.Listening{Threshold: 0.5, Pause: 200 * time.Millisecond, MaxWait: 3 * time.Second, InterruptSpeech: 200 * time.Millisecond, MaxSpeech: 2 * time.Minute}
	if unset.detection != "silero" || unset.listening != want {
		t.Errorf("speech detection = %q with %+v unset; want silero with %+v", unset.detection, unset.listening, want)
	}
	set, err := loadConfig(env(map[string]string{"VOICE_SPEECH_DETECTION": "fake", "VOICE_SPEECH_THRESHOLD": "0.7", "VOICE_PAUSE": "300ms", "VOICE_MAX_WAIT": "2.5s", "VOICE_INTERRUPT_SPEECH": "300ms", "VOICE_MAX_SPEECH": "45s"}))
	want = speech.Listening{Threshold: 0.7, Pause: 300 * time.Millisecond, MaxWait: 2500 * time.Millisecond, InterruptSpeech: 300 * time.Millisecond, MaxSpeech: 45 * time.Second}
	if err != nil || set.detection != "fake" || set.listening != want {
		t.Errorf("speech detection = %q with %+v set (%v); want fake with %+v", set.detection, set.listening, err, want)
	}
}

func TestATurnsSpeechMayRunToTheTwoMinutesTheSpeechToTextServiceHolds(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"VOICE_MAX_SPEECH": "2m"}))
	if err != nil || cfg.listening.MaxSpeech != 2*time.Minute {
		t.Errorf("a turn's speech of 2m: %v (%v), want two minutes accepted", cfg.listening.MaxSpeech, err)
	}
}

func TestSpeechToTextAndTheEndOfTurnCheckAreTheServiceOnThisMachineUnlessSet(t *testing.T) {
	unset, _ := loadConfig(env(nil))
	if unset.sttURL != "ws://127.0.0.1:8092" || unset.turnCheckURL != "http://127.0.0.1:8092" {
		t.Errorf("unset: speech-to-text at %q, the check at %q; want port 8092 on this machine", unset.sttURL, unset.turnCheckURL)
	}
	set, err := loadConfig(env(map[string]string{"VOICE_STT_URL": "wss://stt.example", "VOICE_TURN_CHECK_URL": "https://turn.example"}))
	if err != nil || set.sttURL != "wss://stt.example" || set.turnCheckURL != "https://turn.example" {
		t.Errorf("set: speech-to-text at %q, the check at %q (%v); want each as set", set.sttURL, set.turnCheckURL, err)
	}
}

// .claude/skills/drive-session/start-session.sh sends the placeholder user's document as session
// context; it must fit the default limit, or the script's start is refused.
func TestThePlaceholderUsersDocumentFitsTheSessionContextLimit(t *testing.T) {
	document, err := os.ReadFile("../../seed/placeholder-user/resume.md")
	if err != nil {
		t.Fatalf("read the seed document: %v", err)
	}
	if n := utf8.RuneCount(document); n > defaultSessionContextLimit {
		t.Errorf("the seed document has %d characters, over the default session context limit of %d", n, defaultSessionContextLimit)
	}
}

func TestTheServiceRefusesToStartWithUnsafeSettings(t *testing.T) {
	tests := map[string]map[string]string{
		"no database":                       {"DATABASE_URL": ""},
		"no signing key":                    {"VOICE_TICKET_KEY": ""},
		"a signing key under 32 bytes":      {"VOICE_TICKET_KEY": "short"},
		"no workspace":                      {"VOICE_WORKSPACE_ID": ""},
		"a workspace that isn't a UUID":     {"VOICE_WORKSPACE_ID": "default"},
		"a user context limit of zero":      {"VOICE_USER_CONTEXT_MAX_CHARS": "0"},
		"a user context limit in words":     {"VOICE_USER_CONTEXT_MAX_CHARS": "lots"},
		"a session context limit below one": {"VOICE_SESSION_CONTEXT_MAX_CHARS": "-1"},
		"a session context limit in words":  {"VOICE_SESSION_CONTEXT_MAX_CHARS": "plenty"},
		"a screen input limit of zero":      {"VOICE_SCREEN_INPUT_MAX_CHARS": "0"},
		"a carried limit of zero":           {"VOICE_CARRIED_MAX_CHARS": "0"},
		"a limit on a turn's words of zero": {"VOICE_WORDS_MAX_CHARS": "0"},
		"a model service without a scheme":  {"VOICE_MODEL_URL": "localhost:8091"},
		"a model service on another scheme": {"VOICE_MODEL_URL": "ftp://localhost:8091"},
		"a turn timeout without a unit":     {"VOICE_TURN_TIMEOUT": "75"},
		"a turn timeout of zero":            {"VOICE_TURN_TIMEOUT": "0s"},
		"an audio address that's a name":    {"VOICE_AUDIO_IPS": "localhost"},
		"an audio port of zero":             {"VOICE_AUDIO_UDP_PORT": "0"},
		"an audio port past 65535":          {"VOICE_AUDIO_UDP_PORT": "65536"},
		"an audio port by name":             {"VOICE_AUDIO_UDP_PORT": "webrtc"},
		"an announced address by name":      {"VOICE_AUDIO_IPS": "127.0.0.1", "VOICE_AUDIO_ANNOUNCE_IPS": "localhost"},
		"announcing with no address named":  {"VOICE_AUDIO_ANNOUNCE_IPS": "127.0.0.1"},
		"no audio connections at all":       {"VOICE_MAX_AUDIO_CONNECTIONS": "0"},
		"an unknown text-to-speech":         {"VOICE_TTS": "piper"},
		"Kokoro without a scheme":           {"VOICE_TTS_URL": "localhost:8880"},
		"speech-to-text over plain HTTP":    {"VOICE_STT_URL": "http://localhost:8092"},
		"the end-of-turn check over ws":     {"VOICE_TURN_CHECK_URL": "ws://localhost:8092"},
		"a first-audio wait without a unit": {"VOICE_TTS_FIRST_AUDIO": "5"},
		"a first-audio wait of zero":        {"VOICE_TTS_FIRST_AUDIO": "0s"},
		"an unknown speech detection":       {"VOICE_SPEECH_DETECTION": "webrtc"},
		"a threshold above one":             {"VOICE_SPEECH_THRESHOLD": "1.5"},
		"a threshold of zero":               {"VOICE_SPEECH_THRESHOLD": "0"},
		"a threshold in words":              {"VOICE_SPEECH_THRESHOLD": "half"},
		"a threshold that isn't a number":   {"VOICE_SPEECH_THRESHOLD": "NaN"},
		"a maximum wait over ten seconds":   {"VOICE_MAX_WAIT": "11s"},
		"a pause without a unit":            {"VOICE_PAUSE": "200"},
		"a pause of zero":                   {"VOICE_PAUSE": "0s"},
		"a maximum wait within the pause":   {"VOICE_PAUSE": "3s", "VOICE_MAX_WAIT": "3s"},
		"an interrupt speech of zero":       {"VOICE_INTERRUPT_SPEECH": "0s"},
		"a turn's speech of zero":           {"VOICE_MAX_SPEECH": "0s"},
		"a turn's speech of just 0.2 s":     {"VOICE_MAX_SPEECH": "200ms"},
		"a turn's speech over two minutes":  {"VOICE_MAX_SPEECH": "121s"},
		"a session idle of zero":            {"VOICE_SESSION_IDLE": "0s"},
		"a session idle within a turn":      {"VOICE_SESSION_IDLE": "20s"},
	}
	for name, overrides := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := loadConfig(env(overrides)); err == nil {
				t.Error("loadConfig accepted unsafe settings")
			}
		})
	}
}

func TestTheClientIsServedFromItsBuildFolderUnlessSet(t *testing.T) {
	unset, err := loadConfig(env(nil))
	if err != nil || unset.clientDir != "web/dist-client" {
		t.Errorf("folder = %q (%v) unset, want web/dist-client, built or not", unset.clientDir, err)
	}
	dir := t.TempDir()
	if set, err := loadConfig(env(map[string]string{"VOICE_CLIENT_DIR": dir})); err != nil || set.clientDir != dir {
		t.Errorf("folder = %q (%v) set, want %q", set.clientDir, err, dir)
	}
}

func TestAClientFolderSetToNoFolderStopsTheStart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "voice-guide.js")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(env(map[string]string{"VOICE_CLIENT_DIR": file})); err == nil {
		t.Errorf("VOICE_CLIENT_DIR naming a file loaded, want an error")
	}
	// The cause is kept, so the message says what is wrong with the folder.
	if _, err := loadConfig(env(map[string]string{"VOICE_CLIENT_DIR": filepath.Join(t.TempDir(), "missing")})); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing folder: %v, want an error saying it doesn't exist", err)
	}
}
