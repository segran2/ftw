package appproto

import (
	"errors"
	"testing"

	"github.com/srcfl/ftw/go/internal/config"
)

// memPrefs is the household preference as the command lane's port. It maps
// an allowed export the way the box does, so a test can see that the handler
// stored the permission and did not invent a mode of its own.
type memPrefs struct {
	k      float64
	export string
	calls  int
	err    error
}

func (m *memPrefs) Apply(k *float64, export *string) (PlannerPrefsSnapshot, error) {
	m.calls++
	if m.err != nil {
		return PlannerPrefsSnapshot{}, m.err
	}
	if k != nil {
		m.k = config.ClampSafetyK(*k)
	}
	if export != nil {
		m.export = *export
	}
	mapped := config.BatteryExport(m.export).PlannerModeKey()
	return PlannerPrefsSnapshot{SafetyK: m.k, Export: m.export, MappedMode: mapped}, nil
}

func cmdPlannerPrefs(k float64, export string) Cmd {
	return Cmd{
		CmdID:           "0192f2a0-7c1e-7000-8000-0123456789ac",
		Op:              OpPlannerPrefsSet,
		Args:            map[string]any{"safety_k": k, "battery_export": export},
		NotValidAfterMs: 200_000,
		Expect:          Expect{Rev: 7},
	}
}

func TestPlannerPrefsSetReadsBackTheStoredK(t *testing.T) {
	mem := &memPrefs{}
	h, _, rec, _ := newRigWith(t, mem)
	subscribe(t, h, rec)
	rec.reset()

	deliver(t, h, MsgCmd, nil, cmdPlannerPrefs(0.4, "allowed"))

	res := body[CmdResult](t, rec.only(t, MsgCmdResult))
	if res.State != CmdApplied {
		t.Fatalf("state = %q, want applied", res.State)
	}
	if res.Observed == nil || res.Observed.Value != 0.4 || res.Observed.Src != ObservedSrcCore {
		t.Fatalf("observed = %+v, want k 0.4 from core", res.Observed)
	}
	if mem.calls != 1 || mem.export != "allowed" || mem.k != 0.4 {
		t.Fatalf("stored %+v", mem)
	}
}

func TestPlannerPrefsSetRejectsABadExportBeforeWriting(t *testing.T) {
	mem := &memPrefs{}
	h, _, rec, _ := newRigWith(t, mem)
	subscribe(t, h, rec)
	rec.reset()

	cmd := cmdPlannerPrefs(1, "spicy")
	deliver(t, h, MsgCmd, nil, cmd)

	res := body[CmdResult](t, rec.only(t, MsgCmdResult))
	if res.State != CmdRejected || res.Error == nil || res.Error.Code != ErrUnknownOp {
		t.Fatalf("result = %+v, want a rejected unknown arg", res)
	}
	if mem.calls != 0 {
		t.Fatal("a bad export was written")
	}
}

func TestPlannerPrefsSetWithoutAPortIsUnavailable(t *testing.T) {
	h, _, rec, _ := newRig(t)
	subscribe(t, h, rec)
	rec.reset()

	deliver(t, h, MsgCmd, nil, cmdPlannerPrefs(1, "not_allowed"))

	res := body[CmdResult](t, rec.only(t, MsgCmdResult))
	if res.State != CmdRejected || res.Error == nil || res.Error.Code != ErrUnavailable {
		t.Fatalf("result = %+v, want E_UNAVAILABLE", res)
	}
}

func TestPlannerPrefsSetReportsWhenTheWriteFails(t *testing.T) {
	mem := &memPrefs{err: errors.New("store down")}
	h, _, rec, _ := newRigWith(t, mem)
	subscribe(t, h, rec)
	rec.reset()

	deliver(t, h, MsgCmd, nil, cmdPlannerPrefs(1, "not_allowed"))

	res := body[CmdResult](t, rec.only(t, MsgCmdResult))
	if res.State != CmdRejected || res.Error == nil || res.Error.Code != ErrUnavailable {
		t.Fatalf("result = %+v, want E_UNAVAILABLE", res)
	}
	if mem.calls != 1 {
		t.Fatalf("calls = %d, want the one attempt", mem.calls)
	}
}

func TestPlannerPrefsSetChangesOnePreference(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       map[string]any
		wantK      float64
		wantExport string
	}{
		{"style keeps export", map[string]any{"safety_k": 0.15}, 0.15, "allowed"},
		{"export keeps margin", map[string]any{"battery_export": "not_allowed"}, 0.6, "not_allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := &memPrefs{k: 0.6, export: "allowed"}
			h, _, rec, _ := newRigWith(t, mem)
			subscribe(t, h, rec)
			rec.reset()
			cmd := cmdPlannerPrefs(0, "")
			cmd.Args = tc.args
			deliver(t, h, MsgCmd, nil, cmd)
			res := body[CmdResult](t, rec.only(t, MsgCmdResult))
			if res.State != CmdApplied || mem.k != tc.wantK || mem.export != tc.wantExport {
				t.Fatalf("result=%+v stored=%+v", res, mem)
			}
		})
	}
}

func TestPlannerPrefsSetRejectsEmptyOrInvalidChanges(t *testing.T) {
	for _, args := range []map[string]any{
		{}, {"safety_k": "bold"}, {"safety_k": nil}, {"battery_export": nil},
	} {
		mem := &memPrefs{k: 0.3, export: "not_allowed"}
		h, _, rec, _ := newRigWith(t, mem)
		subscribe(t, h, rec)
		rec.reset()
		cmd := cmdPlannerPrefs(0, "")
		cmd.Args = args
		deliver(t, h, MsgCmd, nil, cmd)
		res := body[CmdResult](t, rec.only(t, MsgCmdResult))
		if res.State != CmdRejected || mem.calls != 0 {
			t.Fatalf("args=%v result=%+v calls=%d", args, res, mem.calls)
		}
	}
}
