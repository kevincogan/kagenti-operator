package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

func TestWorkloadWantsOperatorClientReg(t *testing.T) {
	cases := []struct {
		name        string
		labels      map[string]string
		injectTools bool
		want        bool
	}{
		{
			name: "agent with opt-out",
			labels: map[string]string{
				LabelAgentType:                LabelValueAgent,
				LabelClientRegistrationInject: "false",
			},
			want: true,
		},
		{
			name: "agent without opt-out",
			labels: map[string]string{
				LabelAgentType: LabelValueAgent,
			},
			want: false,
		},
		{
			name: "tool with opt-out and injectTools",
			labels: map[string]string{
				LabelAgentType:                string(agentv1alpha1.RuntimeTypeTool),
				LabelClientRegistrationInject: "false",
			},
			injectTools: true,
			want:        true,
		},
		{
			name: "tool with opt-out no injectTools",
			labels: map[string]string{
				LabelAgentType:                string(agentv1alpha1.RuntimeTypeTool),
				LabelClientRegistrationInject: "false",
			},
			injectTools: false,
			want:        false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workloadWantsOperatorClientReg(tc.labels, tc.injectTools); got != tc.want {
				t.Fatalf("want %v got %v", tc.want, got)
			}
		})
	}
}

func TestInjectOperatorClientRegAnnotation(t *testing.T) {
	pt := &corev1.PodTemplateSpec{}
	if !injectOperatorClientRegAnnotation(pt, "kagenti-opreg-deadbeef") {
		t.Fatal("expected change")
	}
	if pt.Annotations[AnnotationClientRegistrationSecretName] != "kagenti-opreg-deadbeef" {
		t.Fatalf("annotation: %v", pt.Annotations)
	}
	if injectOperatorClientRegAnnotation(pt, "kagenti-opreg-deadbeef") {
		t.Fatal("expected no change")
	}
}

func TestParsePlatformClientIDs(t *testing.T) {
	if got := parsePlatformClientIDs(""); len(got) != 1 || got[0] != "kagenti" {
		t.Fatalf("empty: %v", got)
	}
	if got := parsePlatformClientIDs("a, b"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("list: %v", got)
	}
	if got := parsePlatformClientIDs("  ,  "); len(got) != 1 || got[0] != "kagenti" {
		t.Fatalf("all blank: %v", got)
	}
}

func TestResolveKeycloakClientID(t *testing.T) {
	id, err := resolveKeycloakClientID("ns1", "dep", "", false, "")
	if err != nil || id != "ns1/dep" {
		t.Fatalf("non-spire: %q %v", id, err)
	}
	_, err = resolveKeycloakClientID("ns1", "dep", "", true, "example.org")
	if err == nil {
		t.Fatal("expected error for default SA with SPIRE")
	}
	id, err = resolveKeycloakClientID("ns1", "dep", "mysa", true, "example.org")
	if err != nil || id != "spiffe://example.org/ns/ns1/sa/mysa" {
		t.Fatalf("spire: %q %v", id, err)
	}
}
