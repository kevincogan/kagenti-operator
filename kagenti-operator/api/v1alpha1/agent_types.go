/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentSpec defines the desired state of Agent.
type AgentSpec struct {
	// Description is a human-readable description of the agent
	// +optional
	Description string `json:"description,omitempty"`
	// PodTemplateSpec provides complete control over Pod specification.
	// +required
	PodTemplateSpec *corev1.PodTemplateSpec `json:"podTemplateSpec"`
	// Replicas is the desired number of agent replicas.
	// +optional
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas,omitempty"`
	// Metadata specifies labels and annotations to be added to the agent
	// +optional
	MetadataSpec `json:",inline"`
	// ImageSource specifies the container image or build reference for the agent
	// +required
	ImageSource ImageSource `json:"imageSource"`
	// ServicePorts specifies the service ports to expose for the agent
	// +optional
	// +kubebuilder:default={{name: "http", port: 8080, protocol: "TCP"}}
	ServicePorts []corev1.ServicePort `json:"servicePorts,omitempty"`
}

// +kubebuilder:validation:Union
//
//	ImageSource specifies the container image or build reference for the agent
type ImageSource struct {
	// Image is the container image to use for the agent. This is used when BuildRef is not specified.
	// +optional
	Image *string `json:"image,omitempty"`
	// BuildRef specifies the AgentBuild used for building the agent image from source code.
	// +optional
	BuildRef *BuildRef `json:"buildRef,omitempty"`
}
type BuildRef struct {
	// Name of the AgentBuild resource to use for building the agent image
	// +optional
	Name string `json:"name,omitempty"`
}

type MetadataSpec struct {
	// Labels to be added to this agent
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations to be added to this agent
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// AgentStatus defines the observed state of Agent.
type AgentStatus struct {
	// Conditions represent overall status
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// DeploymentStatus represents the status of the agent deployment
	// +optional
	DeploymentStatus *DeploymentStatus `json:"deploymentStatus,omitempty"`
	// BindingEnforcement tracks identity binding enforcement state
	// +optional
	BindingEnforcement *BindingEnforcementStatus `json:"bindingEnforcement,omitempty"`
}

// BindingEnforcementStatus tracks the state of identity binding enforcement
type BindingEnforcementStatus struct {
	// DisabledByBinding indicates the agent was disabled due to failed identity binding
	DisabledByBinding bool `json:"disabledByBinding,omitempty"`
	// OriginalReplicas stores the replica count before binding enforcement disabled the agent
	// +optional
	OriginalReplicas *int32 `json:"originalReplicas,omitempty"`
	// DisabledAt is when the agent was disabled due to binding enforcement
	// +optional
	DisabledAt *metav1.Time `json:"disabledAt,omitempty"`
	// DisabledReason provides details about why binding enforcement disabled the agent
	// +optional
	DisabledReason string `json:"disabledReason,omitempty"`
}
type LifecyclePhase string

const (
	PhasePending   LifecyclePhase = "Pending"
	PhaseDeploying LifecyclePhase = "Deploying"
	PhaseReady     LifecyclePhase = "Ready"
	PhaseFailed    LifecyclePhase = "Failed"
)

// AgentDeploymentStatus represents the status of the agent deployment
type DeploymentStatus struct {
	// Current deployment phase: PhaseDeploying, PhaseReady, PhaseFailed
	Phase LifecyclePhase `json:"phase,omitempty"`
	// Deployment message
	DeploymentMessage string `json:"deploymentMessage,omitempty"`
	// Deployment completion time
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".spec.replicas",description="Number of Replicas"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.deploymentStatus.phase",description="Deployment Phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Agent is the Schema for the Agents API.
type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentSpec   `json:"spec,omitempty"`
	Status AgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentList contains a list of Agent.
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Agent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Agent{}, &AgentList{})
}
