package terminal

import "testing"

func TestSplitSessionPath(t *testing.T) {
	cases := []struct {
		path     string
		wantID   string
		wantTail string
		wantOK   bool
	}{
		{"/sessions/abc", "abc", "", true},
		{"/sessions/abc/stream", "abc", "stream", true},
		{"/sessions/", "", "", false},
		{"/sessions", "", "", false},
		{"/other/abc", "", "", false},
		{"/sessions/abc/extra/bits", "abc", "extra/bits", true},
	}

	for _, c := range cases {
		id, tail, ok := splitSessionPath(c.path)
		if ok != c.wantOK || id != c.wantID || tail != c.wantTail {
			t.Errorf("splitSessionPath(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.path, id, tail, ok, c.wantID, c.wantTail, c.wantOK)
		}
	}
}
