package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseRemoteHosts(t *testing.T) {
	got := parseRemoteHosts("  tp-g6   lab\ttp-g6 ")
	want := []string{"tp-g6", "lab"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}
	if parseRemoteHosts("") != nil {
		t.Errorf("empty should be nil")
	}
}

func TestRemoteSessionsForHost(t *testing.T) {
	probe := func(host string) ([]string, error) {
		if host == "down" {
			return nil, errors.New("ssh failed")
		}
		if host == "empty" {
			return nil, nil
		}
		return []string{"mono", "nix-config", ""}, nil
	}
	local := map[string]bool{"lab-mono": true}

	sess, ok := remoteSessionsForHost("lab", local, probe)
	if !ok {
		t.Fatal("expected ok")
	}
	if len(sess) != 1 || sess[0] != "nix-config" {
		t.Fatalf("got %v, want [nix-config] (mono suppressed)", sess)
	}

	_, ok = remoteSessionsForHost("down", local, probe)
	if ok {
		t.Fatal("down host should not be ok")
	}

	_, ok = remoteSessionsForHost("empty", local, probe)
	if ok {
		t.Fatal("empty probe should not be ok")
	}
}

func TestCollectRemoteItems(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "lab dead"}
	probe := func(host string) ([]string, error) {
		if host == "dead" {
			return nil, errors.New("unreachable")
		}
		return []string{"mono", "other"}, nil
	}
	local := map[string]bool{"lab-mono": true}

	items := collectRemoteItems(opts, local, probe)
	if len(items) < 3 {
		t.Fatalf("expected header + lab/other + dead bare, got %d: %+v", len(items), items)
	}
	if !items[0].isRemoteHeader {
		t.Fatalf("first row should be remote header")
	}

	var labels []string
	for _, it := range items[1:] {
		if it.remoteHost == "" {
			t.Fatalf("row missing remoteHost: %+v", it)
		}
		labels = append(labels, it.plain)
	}
	joined := strings.Join(labels, " | ")
	if !strings.Contains(joined, "lab/other") {
		t.Errorf("missing lab/other in %q", joined)
	}
	if strings.Contains(joined, "lab/mono") {
		t.Errorf("bridged lab-mono should be suppressed; got %q", joined)
	}
	if !strings.Contains(joined, "dead") || !strings.Contains(joined, "unreachable") {
		t.Errorf("dead host should appear as bare unreachable row; got %q", joined)
	}
}

func TestCollectRemoteItemsEmptyHosts(t *testing.T) {
	if items := collectRemoteItems(nil, nil, nil); items != nil {
		t.Fatalf("no hosts => nil, got %v", items)
	}
}

func TestLocalBridgeSession(t *testing.T) {
	if got := localBridgeSession("tp-g6", "mono"); got != "tp-g6-mono" {
		t.Fatalf("got %q", got)
	}
}
