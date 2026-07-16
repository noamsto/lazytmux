package controlmode

import (
	"reflect"
	"testing"
)

func TestSendKeysArgs(t *testing.T) {
	got := SendKeysArgs("%3", []byte{0x61, 0x62, 0x1b}, 2)
	want := [][]string{
		{"send-keys", "-H", "-t", "%3", "61", "62"},
		{"send-keys", "-H", "-t", "%3", "1b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if len(SendKeysArgs("%3", nil, 2)) != 0 {
		t.Error("empty input should yield no commands")
	}
}
