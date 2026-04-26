package plan

import (
	"bytes"
	"strings"
	"testing"
)

func TestTextWriter(t *testing.T) {
	var buf bytes.Buffer
	w := NewTextWriter(&buf)
	w.Section("Identity bootstrap")
	w.Bullet("admin: %s", "root@pam")
	w.Skip("PIVOT_ENABLED=%v", false)
	got := buf.String()
	want := "\n▸ Identity bootstrap\n    • admin: root@pam\n    ◦ skip: PIVOT_ENABLED=false\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestCapturingWriter(t *testing.T) {
	c := NewCapturingWriter()
	c.Section("A")
	c.Bullet("a1=%d", 1)
	c.Skip("a-skipped")
	c.Section("B")
	c.Bullet("b1")
	if len(c.Events) != 5 {
		t.Fatalf("got %d events, want 5", len(c.Events))
	}
	if got, want := strings.Join(c.Sections(), "|"), "A|B"; got != want {
		t.Fatalf("Sections() = %q, want %q", got, want)
	}
	if c.Events[1].Kind != EventBullet || c.Events[1].Text != "a1=1" {
		t.Fatalf("Events[1] = %+v, want Bullet a1=1", c.Events[1])
	}
	if c.Events[2].Kind != EventSkip || c.Events[2].Text != "a-skipped" {
		t.Fatalf("Events[2] = %+v, want Skip a-skipped", c.Events[2])
	}
}

func TestDiscardWriter(t *testing.T) {
	w := NewDiscardWriter()
	w.Section("ignored")
	w.Bullet("ignored %s", "too")
	w.Skip("ignored %s", "again")
	// Just verifying no panic and no return value to inspect.
}
