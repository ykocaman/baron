package cli

// dataflow is the declared data-flow statement: what crosses the machine
// boundary and where it goes. Carried on Init's result.
type dataflow struct {
	LocalFirst bool       `json:"local_first"`
	Telemetry  string     `json:"telemetry"`
	Flows      []dataEdge `json:"flows"`
	// ActualRemote is the real `git remote get-url origin`, when a caller
	// wants the declaration checked against reality. Empty when not
	// verified (Init's own declaration doesn't set it).
	ActualRemote string `json:"actual_remote,omitempty"`
}

// dataEdge names one data path from source to destination.
type dataEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	What string `json:"what"`
}

// dataflowDeclaration returns the canonical data-flow statement. Keep this
// in sync with what baron actually does — 's acceptance
// criterion is that the declaration and real traffic match.
func dataflowDeclaration() dataflow {
	return dataflow{
		LocalFirst: true,
		Telemetry:  "off by default; opt-in only",
		Flows: []dataEdge{
			{From: "local beads db", To: "git remote (refs/dolt/data)", What: "issue data, via bd dolt push/pull"},
			{From: "baron", To: "model CLI subprocess", What: "bead brief + your credentials; official CLI only"},
			{From: "gate steps", To: "local processes", What: "credential-isolated; nothing leaves the machine"},
			{From: "baron", To: "local audit log", What: "actor, action, target (never secrets)"},
		},
	}
}
