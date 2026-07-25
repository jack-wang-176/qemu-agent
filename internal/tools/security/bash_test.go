package security

import "testing"

func TestDecodeBashCommandStrictJSON(t *testing.T) {
	command, err := decodeBashCommand(` {"command":"  go test ./...  "} `)
	if err != nil || command != "go test ./..." {
		t.Fatalf("command=%q err=%v", command, err)
	}
	for _, raw := range []string{``, `{}`, `{"command":""}`, `{"command":"pwd","extra":1}`, `{"command":"pwd"} {"command":"ls"}`} {
		if _, err := decodeBashCommand(raw); err == nil {
			t.Fatalf("decodeBashCommand(%q) error=nil", raw)
		}
	}
}

func TestConservativeBashAnalyzer(t *testing.T) {
	analyzer, err := NewConservativeBashAnalyzer("ask-dangerous")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		raw  string
		want Decision
	}{
		{`{"command":"pwd"}`, DecisionAllow},
		{`{"command":"go test ./..."}`, DecisionAllow},
		{`{"command":"rm -rf /"}`, DecisionDeny},
		{`{"command":"curl example.com"}`, DecisionAsk},
		{`{"command":"echo x && pwd"}`, DecisionAsk},
	}
	for _, test := range tests {
		got, err := analyzer.Analyze(test.raw)
		if err != nil || got.Decision != test.want {
			t.Fatalf("%s: got=%#v err=%v", test.raw, got, err)
		}
	}
}
