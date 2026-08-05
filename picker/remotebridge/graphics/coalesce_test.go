package graphics

import "testing"

func store(id, path string) Chunk {
	return Chunk{Seq: seqLit("\x1b_Gi=" + id + ",a=T,U=1,f=100,t=f;" + path + "\x1b\\")}
}

func del(id string) Chunk {
	return Chunk{Seq: seqLit("\x1b_Ga=d,d=I,i=" + id + ",q=2\x1b\\")}
}

func seqLit(raw string) *Seq {
	return NewScanner().Feed([]byte(raw))[0].Seq
}

func TestCoalesceKeepsOnlyTheNewestStorePerID(t *testing.T) {
	in := []Chunk{store("1", "YQ=="), store("1", "Yg=="), store("2", "Yw==")}
	out := Coalesce(in)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if string(out[0].Seq.Payload) != "Yg==" {
		t.Fatalf("kept the stale frame: %q", out[0].Seq.Payload)
	}
}

func TestCoalesceNeverCrossesADelete(t *testing.T) {
	in := []Chunk{store("1", "YQ=="), del("1"), store("1", "Yg==")}
	if out := Coalesce(in); len(out) != 3 {
		t.Fatalf("len = %d, want 3 — a delete is a real transition, not a superseded frame", len(out))
	}
}

// A delete is a real state transition, never a stale frame in its own right —
// distinct from TestCoalesceNeverCrossesADelete, which checks that a delete
// stops an EARLIER store from being coalesced away. This checks that the
// delete itself is never the thing dropped, regardless of what surrounds it.
func TestCoalesceNeverDropsADelete(t *testing.T) {
	in := []Chunk{del("1"), del("1"), store("1", "YQ=="), del("1")}
	out := Coalesce(in)
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4 — a delete must survive Coalesce unconditionally: %+v", len(out), out)
	}
}

func TestCoalescePreservesLiteralsAndOrder(t *testing.T) {
	in := []Chunk{{Literal: []byte("a")}, store("1", "YQ=="), {Literal: []byte("b")}, store("1", "Yg==")}
	out := Coalesce(in)
	if len(out) != 3 || string(out[0].Literal) != "a" || string(out[1].Literal) != "b" {
		t.Fatalf("literals reordered or lost: %+v", out)
	}
	if string(out[2].Seq.Payload) != "Yg==" {
		t.Fatalf("wrong survivor: %q", out[2].Seq.Payload)
	}
}
