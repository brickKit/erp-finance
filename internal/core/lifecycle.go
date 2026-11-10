package core

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A lifecycle is one table: every phase × every command is one cell saying what the command does there (the
// requisition's and the order's, docs/design.md). Each module keeps its table and its one writer of the state; the
// checks here keep a table whole, the same as the design document's, and named by a test.

// Effect is what a command does in a phase.
type Effect string

const (
	Refuse  Effect = "refuse"  // refused with Decision.Refusal (STATE_CONFLICT)
	Ignore  Effect = "ignore"  // nothing happens; the command answers the record as it is (a repeat)
	Proceed Effect = "proceed" // the command does its work; the state stays
	Move    Effect = "move"    // the command does its work and the record moves to Decision.To
)

// Decision is one cell.
type Decision struct {
	Rule    string // the rule in docs/design.md
	Effect  Effect
	To      string // Move: the state after
	Op      string // Move: the operation the audit names ("" not audited)
	Refusal string // Refuse: the words; {state} is the record's
	Guard   string // a further condition the command checks before it acts, "" none
}

// Table is a lifecycle: its phases and commands in the order of docs/design.md, and its cells.
type Table struct {
	Rule     *regexp.Regexp // the form of its rules' names, e.g. ^PO\d\d$
	Phases   []string
	Commands []string
	Cells    map[string]map[string]Decision // command → phase → decision
}

// Decide answers what command c does in phase p.
func (t Table) Decide(p, c string) Decision {
	d, ok := t.Cells[c][p]
	if !ok { // TableProblems keeps this from happening
		return Decision{Effect: Refuse, Refusal: fmt.Sprintf("no rule for %s in %s", c, p)}
	}
	return d
}

// Refused is d's refusal for a record in state, as the STATE_CONFLICT the user surface answers.
func Refused(d Decision, state string) error {
	return ErrState.New(strings.ReplaceAll(d.Refusal, "{state}", state))
}

func RefuseCell(rule, words string) Decision {
	return Decision{Rule: rule, Effect: Refuse, Refusal: words}
}
func IgnoreCell(rule string) Decision  { return Decision{Rule: rule, Effect: Ignore} }
func ProceedCell(rule string) Decision { return Decision{Rule: rule, Effect: Proceed} }
func MoveCell(rule, to, op string) Decision {
	return Decision{Rule: rule, Effect: Move, To: to, Op: op}
}

// TableProblems says what is missing from t: a cell not decided, a rule not named, a refusal without words, a move
// to a state that is not a phase.
func TableProblems(t Table) []string {
	var out []string
	if len(t.Cells) != len(t.Commands) {
		out = append(out, fmt.Sprintf("the table has %d commands, Commands %d", len(t.Cells), len(t.Commands)))
	}
	phase := map[string]bool{}
	for _, p := range t.Phases {
		phase[p] = true
	}
	for _, c := range t.Commands {
		if len(t.Cells[c]) != len(t.Phases) {
			out = append(out, fmt.Sprintf("%s decides %d phases, not %d", c, len(t.Cells[c]), len(t.Phases)))
		}
		for _, p := range t.Phases {
			d, ok := t.Cells[c][p]
			switch {
			case !ok:
				out = append(out, c+" in "+p+" is not decided")
			case !t.Rule.MatchString(d.Rule):
				out = append(out, c+" in "+p+" names no rule")
			case d.Effect == Refuse && d.Refusal == "":
				out = append(out, c+" in "+p+": a refusal has no words")
			case d.Effect == Move && !phase[d.To]:
				out = append(out, c+" in "+p+": moves to "+d.To+", not a phase")
			case d.Effect != Refuse && d.Effect != Ignore && d.Effect != Proceed && d.Effect != Move:
				out = append(out, c+" in "+p+": unknown effect "+string(d.Effect))
			}
		}
	}
	return out
}

// DocumentProblems compares t with the table in doc whose header names t's phases (| 命令 | `draft` | …), cell by
// cell: 允许 (proceed), 拒绝 (refuse), 忽略 (ignore), → `state` (move), each followed by its rule; a cell may start
// with words before the effect (a guard, said in the document).
func DocumentProblems(t Table, doc string) []string {
	header := "| 命令 | `" + strings.Join(t.Phases, "` | `") + "` |"
	i := strings.Index(doc, header)
	if i < 0 {
		return []string{"the document has no table " + header}
	}
	cell := regexp.MustCompile("(允许|拒绝|忽略|→ `(\\w+)`)[^|]*? (" + strings.Trim(t.Rule.String(), "^$") + ")$")
	tick := regexp.MustCompile("`(\\w+)`")
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(doc[i:], "\n")[2:] {
		if !strings.HasPrefix(line, "| ") {
			break
		}
		cells := strings.Split(strings.Trim(line, "| "), " | ")
		names := tick.FindAllStringSubmatch(cells[0], -1)
		if len(names) == 0 || len(cells) != len(t.Phases)+1 {
			out = append(out, "a row the check does not read: "+line)
			continue
		}
		c := names[len(names)-1][1]
		if _, ok := t.Cells[c]; !ok {
			out = append(out, "the document has a command the code does not: "+c)
			continue
		}
		seen[c] = true
		for k, p := range t.Phases {
			m := cell.FindStringSubmatch(cells[k+1])
			if m == nil {
				out = append(out, fmt.Sprintf("%s in %s: %q is not a cell", c, p, cells[k+1]))
				continue
			}
			got := Decision{Rule: m[3]}
			switch m[1] {
			case "允许":
				got.Effect = Proceed
			case "拒绝":
				got.Effect = Refuse
			case "忽略":
				got.Effect = Ignore
			default:
				got.Effect, got.To = Move, m[2]
			}
			want := t.Cells[c][p]
			if want.Rule != got.Rule || want.Effect != got.Effect || want.To != got.To {
				out = append(out, fmt.Sprintf("%s in %s: the code says %s %s %s, the document %s %s %s", c, p,
					want.Effect, want.To, want.Rule, got.Effect, got.To, got.Rule))
			}
		}
	}
	for _, c := range t.Commands {
		if !seen[c] {
			out = append(out, "the document does not decide "+c)
		}
	}
	return out
}

// UntestedRules are the rules of t no integration test names (a comment or words in dir's *_test.go).
func UntestedRules(t Table, dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	var all strings.Builder
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		all.Write(b)
	}
	named := map[string]bool{}
	for _, r := range regexp.MustCompile(`\b`+strings.Trim(t.Rule.String(), "^$")+`\b`).FindAllString(all.String(), -1) {
		named[r] = true
	}
	var out []string
	for _, c := range t.Commands {
		for _, p := range t.Phases {
			if r := t.Cells[c][p].Rule; !named[r] {
				out = append(out, r+" ("+c+" in "+p+")")
			}
		}
	}
	return out, nil
}
