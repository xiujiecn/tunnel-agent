package lib

import "testing"

func TestWantGUI(t *testing.T) {
	cases := []struct {
		name   string
		gui    bool
		server string
		token  string
		want   bool
	}{
		{"无参数", false, "", "", true},
		{"显式-gui", true, "wss://x", "t", true},
		{"完整CLI", false, "wss://x", "tok", false},
		{"缺token仍CLI", false, "wss://x", "", false},
		{"缺server仍CLI", false, "", "tok", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WantGUI(tc.gui, tc.server, tc.token); got != tc.want {
				t.Fatalf("WantGUI() = %v, want %v", got, tc.want)
			}
		})
	}
}
