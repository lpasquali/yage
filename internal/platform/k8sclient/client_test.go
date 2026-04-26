package k8sclient

import "testing"

func TestSplitYAMLDocs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"single", "apiVersion: v1\nkind: Namespace\n", 1},
		{"two", "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: a\n---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: b\n", 2},
		{"trailing-sep", "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: a\n---\n", 2},
		{"empty", "", 1},
		{"only-sep", "---", 1},
		{"three-with-blank-middle", "a: 1\n---\n\n---\nb: 2\n", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitYAMLDocs([]byte(tt.in))
			if len(got) != tt.want {
				t.Fatalf("splitYAMLDocs %q: got %d docs, want %d (%q)", tt.in, len(got), tt.want, got)
			}
		})
	}
}

func TestBoolPtr(t *testing.T) {
	tr := boolPtr(true)
	if tr == nil || *tr != true {
		t.Fatalf("boolPtr(true) returned %v", tr)
	}
	fa := boolPtr(false)
	if fa == nil || *fa != false {
		t.Fatalf("boolPtr(false) returned %v", fa)
	}
	if tr == fa {
		t.Fatalf("boolPtr should return distinct pointers")
	}
}

func TestStrPtr(t *testing.T) {
	s := strPtr("hello")
	if s == nil || *s != "hello" {
		t.Fatalf("strPtr returned %v", s)
	}
}

// Note: ContextExists / CurrentContext / ListContexts depend on
// $KUBECONFIG and ~/.kube/config. We verify the no-kubeconfig case
// produces sane defaults rather than panicking, but skip the full path
// to avoid coupling tests to the developer's kubeconfig.
func TestContextHelpersTolerateMissingKubeconfig(t *testing.T) {
	t.Setenv("KUBECONFIG", "/nonexistent/path/does/not/exist")
	t.Setenv("HOME", t.TempDir())
	if got := CurrentContext(); got != "" {
		t.Errorf("CurrentContext with no kubeconfig: got %q, want empty", got)
	}
	if got := ListContexts(); len(got) != 0 {
		t.Errorf("ListContexts with no kubeconfig: got %v, want empty", got)
	}
	if ContextExists("anything") {
		t.Errorf("ContextExists with no kubeconfig: returned true, want false")
	}
}
