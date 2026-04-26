package awsebs

import "testing"

func TestDriverConstants(t *testing.T) {
	d := driver{}
	if got, want := d.Name(), "aws-ebs"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if got, want := d.K8sCSIDriverName(), "ebs.csi.aws.com"; got != want {
		t.Errorf("K8sCSIDriverName() = %q, want %q", got, want)
	}
	if got, want := d.DefaultStorageClass(), "ebs-gp3"; got != want {
		t.Errorf("DefaultStorageClass() = %q, want %q", got, want)
	}
	defs := d.Defaults()
	if len(defs) != 1 || defs[0] != "aws" {
		t.Errorf("Defaults() = %v, want [aws]", defs)
	}
}

func TestHelmChart(t *testing.T) {
	d := driver{}
	repo, chart, ver, err := d.HelmChart(nil)
	if err != nil {
		t.Fatalf("HelmChart() unexpected err: %v", err)
	}
	if chart != "aws-ebs-csi-driver" {
		t.Errorf("chart = %q", chart)
	}
	if ver != "v2.32.0" {
		t.Errorf("version = %q", ver)
	}
	if repo == "" {
		t.Errorf("repo must not be empty")
	}
}
