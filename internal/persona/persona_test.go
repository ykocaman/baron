package persona

import "testing"

func TestPersonaValidate(t *testing.T) {
	tests := []struct {
		name    string
		p       Persona
		wantErr bool
	}{
		{"valid", Persona{ID: "qa", Name: "QA", Prompt: "check things", Trigger: Trigger{}}, false},
		{"empty id", Persona{Name: "QA", Prompt: "x", Trigger: Trigger{}}, true},
		{"empty name", Persona{ID: "qa", Prompt: "x", Trigger: Trigger{}}, true},
		{"empty prompt", Persona{ID: "qa", Name: "QA", Trigger: Trigger{}}, true},
		{"invalid cron schedule", Persona{ID: "qa", Name: "QA", Prompt: "x", Trigger: Trigger{Schedule: "whenever"}}, true},
		{"valid cron schedule", Persona{ID: "qa", Name: "QA", Prompt: "x", Trigger: Trigger{Schedule: "0 9 * * *"}}, false},
		{"transition rule missing to", Persona{ID: "qa", Name: "QA", Prompt: "x", Trigger: Trigger{On: []TransitionRule{{From: "*"}}}}, true},
		{"valid transition rule", Persona{ID: "qa", Name: "QA", Prompt: "x", Trigger: Trigger{On: []TransitionRule{{From: "*", To: "merged"}}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.p.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseTransitionRules(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []TransitionRule
		wantErr bool
	}{
		{"empty", "", nil, false},
		{"whitespace only", "   ", nil, false},
		{"single ascii arrow", "*->merged", []TransitionRule{{From: "*", To: "merged"}}, false},
		{"multiple with spacing", " *->merged , working -> retry ", []TransitionRule{{From: "*", To: "merged"}, {From: "working", To: "retry"}}, false},
		{"unicode arrow", "*→closed", []TransitionRule{{From: "*", To: "closed"}}, false},
		{"trailing comma skipped", "*->merged,", []TransitionRule{{From: "*", To: "merged"}}, false},
		{"missing arrow", "merged", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTransitionRules(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseTransitionRules(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ParseTransitionRules(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ParseTransitionRules(%q)[%d] = %+v, want %+v", tt.in, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestTriggerEventsTextRoundTrip(t *testing.T) {
	tr := Trigger{On: []TransitionRule{{From: "*", To: "merged"}, {From: "working", To: "retry"}}}
	text := tr.EventsText()
	got, err := ParseTransitionRules(text)
	if err != nil {
		t.Fatalf("ParseTransitionRules(%q): %v", text, err)
	}
	if len(got) != len(tr.On) {
		t.Fatalf("round-trip = %+v, want %+v", got, tr.On)
	}
	for i := range got {
		if got[i] != tr.On[i] {
			t.Errorf("round-trip[%d] = %+v, want %+v", i, got[i], tr.On[i])
		}
	}
}

func TestPersonaInstalled(t *testing.T) {
	tests := []struct {
		source Source
		want   bool
	}{
		{SourceBuiltin, false},
		{SourceUser, false},
		{"installed:community-qa", true},
	}
	for _, tt := range tests {
		p := Persona{Source: tt.source}
		if got := p.Installed(); got != tt.want {
			t.Errorf("Persona{Source: %q}.Installed() = %v, want %v", tt.source, got, tt.want)
		}
	}
}
