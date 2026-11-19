package agentfile_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mikaclements/voice-guide/internal/agents"
	"github.com/mikaclements/voice-guide/internal/agents/agentfile"
)

const (
	keeperID  = "01a1de8a-0000-7000-8000-0000000000a1"
	captainID = "01a1de8a-0000-7000-8000-0000000000a2"
)

// agentFile builds an agent file: a settings block, then the persona.
func agentFile(settings, persona string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("---\n" + settings + "---\n" + persona)}
}

// settings builds a complete, valid settings block.
func settings(id, name string) string {
	return "id: " + id + "\nname: " + name + "\ndescription: Talks ships through the fog.\nreads: [profile]\n"
}

func TestLoadParsesSettingsAndPersona(t *testing.T) {
	got, err := agentfile.Load(context.Background(), fstest.MapFS{
		"lighthouse-keeper.md": agentFile(settings(keeperID, "Lighthouse keeper"), "You answer every call from the sea.\n"),
	})
	if err != nil || len(got) != 1 {
		t.Fatalf("Load = %d agents, %v; want 1", len(got), err)
	}
	a := got[0]
	if a.ID.String() != keeperID || a.Name != "Lighthouse keeper" || a.Description != "Talks ships through the fog." {
		t.Errorf("settings = %+v", a)
	}
	if !slices.Equal(a.Reads, []string{"profile"}) || a.Persona != "You answer every call from the sea.\n" {
		t.Errorf("reads %v, persona %q", a.Reads, a.Persona)
	}
}

func TestLoadParsesTheSessionContextAProductMustSend(t *testing.T) {
	got, err := agentfile.Load(context.Background(), fstest.MapFS{
		"lighthouse-keeper.md": agentFile(settings(keeperID, "Lighthouse keeper")+"session_context: [resume, current_page]\n", "You answer every call from the sea.\n"),
	})
	if err != nil || len(got) != 1 || !slices.Equal(got[0].SessionContext, []string{"resume", "current_page"}) {
		t.Errorf("Load = %+v, %v; want the session context, in order", got, err)
	}
}

func TestLoadParsesTheSummaryInstructionWithoutTheBlocksLastLineBreak(t *testing.T) {
	got, err := agentfile.Load(context.Background(), fstest.MapFS{
		"lighthouse-keeper.md": agentFile(settings(keeperID, "Lighthouse keeper")+"summary: |\n  Note the ships you guided.\n  Name any that ran aground.\n", "You answer every call from the sea.\n"),
	})
	if want := "Note the ships you guided.\nName any that ran aground."; err != nil || len(got) != 1 || got[0].Summary != want {
		t.Errorf("Load = %+v, %v; want the summary instruction %q", got, err, want)
	}
}

func TestLoadReadsWhoOpensTheSessionTheUserUnlessSet(t *testing.T) {
	for _, tt := range []struct {
		setting string
		opens   bool
	}{{"", false}, {"opens: user\n", false}, {"opens: agent\n", true}} {
		got, err := agentfile.Load(context.Background(), fstest.MapFS{
			"lighthouse-keeper.md": agentFile(settings(keeperID, "Lighthouse keeper")+tt.setting, "You answer every call from the sea.\n"),
		})
		if err != nil || len(got) != 1 || got[0].Opens != tt.opens {
			t.Errorf("Load with %q = %+v, %v; want Opens %v", tt.setting, got, err, tt.opens)
		}
	}
}

func TestLoadRefusesMalformedAgentFiles(t *testing.T) {
	valid := settings(keeperID, "Lighthouse keeper")
	tests := []struct {
		name string
		fsys fstest.MapFS
	}{
		{"missing ID", fstest.MapFS{"keeper.md": agentFile("name: Lighthouse keeper\n", "p")}},
		{"ID that isn't a UUID", fstest.MapFS{"keeper.md": agentFile("id: keeper\nname: Lighthouse keeper\n", "p")}},
		{"unknown setting", fstest.MapFS{"keeper.md": agentFile(valid+"temperature: 0.2\n", "p")}},
		{"opened by neither the agent nor the user", fstest.MapFS{"keeper.md": agentFile(valid+"opens: both\n", "p")}},
		{"malformed settings", fstest.MapFS{"keeper.md": agentFile("id: [\n", "p")}},
		{"no settings block", fstest.MapFS{"keeper.md": {Data: []byte("You answer every call from the sea.\n")}}},
		{"unclosed settings block", fstest.MapFS{"keeper.md": {Data: []byte("---\n" + valid)}}},
		{"file that isn't Markdown", fstest.MapFS{"keeper.txt": agentFile(valid, "p")}},
		{"folder", fstest.MapFS{"harbour/keeper.md": agentFile(valid, "p")}},
		{"no agent files", fstest.MapFS{".DS_Store": {Data: []byte("finder")}}},
		{"one ID in two files", fstest.MapFS{
			"keeper.md":  agentFile(valid, "p"),
			"captain.md": agentFile(settings(keeperID, "Ferry captain"), "p"),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := agentfile.Load(context.Background(), tt.fsys); err == nil {
				t.Error("Load accepted a malformed agent file")
			}
		})
	}
}

func TestTheRepositorysAgentsLoad(t *testing.T) {
	got, err := agentfile.Load(context.Background(), os.DirFS("../../../agents"))
	if err != nil || len(got) == 0 {
		t.Fatalf("Load(agents/) = %d agents, %v", len(got), err)
	}
	if err := agents.Check(got); err != nil {
		t.Errorf("agents/ fails publishing's checks: %v", err)
	}
}

func TestLoadSkipsHiddenFiles(t *testing.T) {
	fsys := fstest.MapFS{
		".DS_Store":    {Data: []byte("finder")},
		".keeper.swp":  {Data: []byte("editor")},
		"keeper.md":    agentFile(settings(keeperID, "Lighthouse keeper"), "p"),
		"captain.md":   agentFile(settings(captainID, "Ferry captain"), "p"),
		".git-keep.md": {Data: []byte("not an agent")},
	}
	got, err := agentfile.Load(context.Background(), fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("Load returned %d agents, want 2", len(got))
	}
}

func TestLoadStopsWhenItsContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := agentfile.Load(ctx, fstest.MapFS{"keeper.md": agentFile(settings(keeperID, "Lighthouse keeper"), "p")}); !errors.Is(err, context.Canceled) {
		t.Errorf("Load error = %v, want context.Canceled", err)
	}
}

func TestLoadReadsTheModelAndRefusesOneThatIsNotAModelName(t *testing.T) {
	load := func(setting string) ([]agents.Agent, error) {
		return agentfile.Load(context.Background(), fstest.MapFS{
			"lighthouse-keeper.md": agentFile(settings(keeperID, "Lighthouse keeper")+setting, "You answer every call from the sea.\n"),
		})
	}
	for setting, want := range map[string]string{"": "", "model: claude-sonnet-5-5\n": "claude-sonnet-5-5", "model: gpt.4-o\n": "gpt.4-o"} {
		if got, err := load(setting); err != nil || len(got) != 1 || got[0].Model != want {
			t.Errorf("Load with %q = %+v, %v; want Model %q", setting, got, err, want)
		}
	}
	for _, bad := range []string{"Claude-Sonnet", "-sonnet", "sonnet 5", "sonnet/5", strings.Repeat("a", 65)} {
		if _, err := load("model: " + bad + "\n"); err == nil || !strings.Contains(err.Error(), "lighthouse-keeper.md: model") {
			t.Errorf("Load with model %q = %v; want an error naming the file and the model", bad, err)
		}
	}
}

func TestLoadReadsTheVoiceAndSpeedAndRefusesAVoiceOutOfFormOrASpeedOutOfRange(t *testing.T) {
	load := func(setting string) ([]agents.Agent, error) {
		return agentfile.Load(context.Background(), fstest.MapFS{
			"lighthouse-keeper.md": agentFile(settings(keeperID, "Lighthouse keeper")+setting, "You answer every call from the sea.\n"),
		})
	}
	for setting, want := range map[string]agents.Agent{
		"":                             {},
		"voice: bf_emma\n":             {Voice: "bf_emma"},
		"speed: 1.25\n":                {Speed: 1.25},
		"voice: am_adam\nspeed: 0.5\n": {Voice: "am_adam", Speed: 0.5},
		"speed: 2\n":                   {Speed: 2},
	} {
		if got, err := load(setting); err != nil || len(got) != 1 || got[0].Voice != want.Voice || got[0].Speed != want.Speed {
			t.Errorf("Load with %q = %+v, %v; want voice %q, speed %v", setting, got, err, want.Voice, want.Speed)
		}
	}
	for _, bad := range []string{"voice: ../af_heart\n", "voice: AF_heart\n", "voice: af_heart+bf_emma\n", "speed: 0.49\n", "speed: 2.01\n", "speed: 0\n", "speed: -1\n", "speed: .nan\n", "speed: .inf\n"} {
		if _, err := load(bad); err == nil || !strings.Contains(err.Error(), "lighthouse-keeper.md: ") {
			t.Errorf("Load with %q = %v; want an error naming the file", bad, err)
		}
	}
}

// gradedSummary is a summary section in its mapping form, with a model, a scale, levels and criteria.
const gradedSummary = `summary:
  instruction: |
    Note the ships you guided.
  model: claude-opus-5-5
  scale: [1, 5]
  levels:
    5: Guided every ship home.
    1: Lost a ship.
  criteria:
    - name: fog_calls
      looks_for: Answers every call in the fog.
    - name: lamp_care
      looks_for: |
        Keeps the lamp lit all night.
`

func TestLoadReadsTheSummaryMappingWithItsModelScaleAndCriteria(t *testing.T) {
	got, err := agentfile.Load(context.Background(), fstest.MapFS{
		"lighthouse-keeper.md": agentFile(settings(keeperID, "Lighthouse keeper")+gradedSummary, "You answer every call from the sea.\n"),
	})
	if err != nil || len(got) != 1 {
		t.Fatalf("Load = %d agents, %v; want 1", len(got), err)
	}
	a := got[0]
	want := agents.Grading{
		Lowest: 1, Highest: 5,
		Levels:   []agents.Level{{Score: 1, Means: "Lost a ship."}, {Score: 5, Means: "Guided every ship home."}},
		Criteria: []agents.Criterion{{Name: "fog_calls", LooksFor: "Answers every call in the fog."}, {Name: "lamp_care", LooksFor: "Keeps the lamp lit all night."}},
	}
	if a.Summary != "Note the ships you guided." || a.SummaryModel != "claude-opus-5-5" || a.Model != "" {
		t.Errorf("summary %q, summary model %q, model %q", a.Summary, a.SummaryModel, a.Model)
	}
	if a.Grading.Lowest != want.Lowest || a.Grading.Highest != want.Highest ||
		!slices.Equal(a.Grading.Levels, want.Levels) || !slices.Equal(a.Grading.Criteria, want.Criteria) {
		t.Errorf("grading = %+v, want %+v (levels in score order)", a.Grading, want)
	}
	if err := agents.Check(got); err != nil {
		t.Errorf("Check = %v, want the loaded agent accepted", err)
	}
}

func TestLoadReadsASummaryMappingWithTheInstructionAloneAsAPlainString(t *testing.T) {
	load := func(summary string) agents.Agent {
		t.Helper()
		got, err := agentfile.Load(context.Background(), fstest.MapFS{
			"lighthouse-keeper.md": agentFile(settings(keeperID, "Lighthouse keeper")+summary, "You answer every call from the sea.\n"),
		})
		if err != nil || len(got) != 1 {
			t.Fatalf("Load with %q = %+v, %v", summary, got, err)
		}
		return got[0]
	}
	plain := load("summary: Note the ships you guided.\n")
	mapped := load("summary:\n  instruction: Note the ships you guided.\n")
	if plain.Summary != mapped.Summary || mapped.SummaryModel != "" || mapped.Grading.IsSet() {
		t.Errorf("plain %+v, mapping %+v; want the same instruction and nothing else", plain, mapped)
	}
}

func TestLoadRefusesASummaryMappingOutOfForm(t *testing.T) {
	const criteria = "  criteria:\n    - name: fog_calls\n      looks_for: Answers every call.\n"
	for name, summary := range map[string]string{
		"no instruction":           "summary:\n  model: claude-opus-5-5\n",
		"an unknown key":           "summary:\n  instruction: Note it.\n  temperature: 0.2\n",
		"an unknown criterion key": "summary:\n  instruction: Note it.\n  scale: [1, 5]\n  criteria:\n    - name: fog_calls\n      weight: 2\n      looks_for: Answers.\n",
		"a summary model's name":   "summary:\n  instruction: Note it.\n  model: Claude Opus\n",
		"a scale without criteria": "summary:\n  instruction: Note it.\n  scale: [1, 5]\n",
		"levels without criteria":  "summary:\n  instruction: Note it.\n  levels:\n    1: Lost.\n",
		"criteria without a scale": "summary:\n  instruction: Note it.\n" + criteria,
		"a scale of one end":       "summary:\n  instruction: Note it.\n  scale: [5]\n" + criteria,
		"a scale of three":         "summary:\n  instruction: Note it.\n  scale: [1, 3, 5]\n" + criteria,
		"a scale of words":         "summary:\n  instruction: Note it.\n  scale: [low, high]\n" + criteria,
		"a list":                   "summary: [Note it.]\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := agentfile.Load(context.Background(), fstest.MapFS{
				"lighthouse-keeper.md": agentFile(settings(keeperID, "Lighthouse keeper")+summary, "p"),
			})
			if err == nil || !strings.Contains(err.Error(), "lighthouse-keeper.md: ") {
				t.Errorf("Load = %v; want an error naming the file", err)
			}
		})
	}
}
