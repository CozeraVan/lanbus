package scanner

import "testing"

func TestParsePorts(t *testing.T) {
	ports := parsePorts("11434,1234,8000-8002", false)
	want := map[int]bool{11434: true, 1234: true, 8000: true, 8001: true, 8002: true}
	for p := range want {
		found := false
		for _, x := range ports {
			if x == p {
				found = true
			}
		}
		if !found {
			t.Fatalf("port %d missing in %v", p, ports)
		}
	}
}

func TestGuessOS(t *testing.T) {
	if guessOS([]int{22, 5000, 7000}) != "macos" {
		t.Fatal("expected macos")
	}
	if guessOS([]int{135, 445, 3389}) != "windows" {
		t.Fatal("expected windows")
	}
	if guessOS([]int{22}) != "unix" {
		t.Fatal("expected unix")
	}
}
