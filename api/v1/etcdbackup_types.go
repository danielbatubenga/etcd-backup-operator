package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EtcdBackupSpec define o que o usuário pode configurar
type EtcdBackupSpec struct {
	// NodeName é o nome do nó onde o etcd está rodando (geralmente o control-plane)
	// Se vazio, o operador tentará determinar automaticamente.
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// DestinationPath é o diretório no nó onde o backup .zip será salvo.
	// Padrão: "/home"
	// +optional
	DestinationPath string `json:"destinationPath,omitempty"`
}


type EtcdBackupStatus struct {
	// Conditions representam as condições mais recentes (Started, Completed, Failed)
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// BackupPath é o caminho completo do arquivo zip gerado no nó (preenchido após conclusão)
	// +optional
	BackupPath string `json:"backupPath,omitempty"`

	// StartTime é o timestamp de início do backup
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime é o timestamp de conclusão do backup
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
}

// EtcdBackup é o Schema para o recurso de backup do etcd
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Node",type="string",JSONPath=".spec.nodeName"
//+kubebuilder:printcolumn:name="Started",type="date",JSONPath=".status.startTime"
//+kubebuilder:printcolumn:name="Completed",type="date",JSONPath=".status.completionTime"
type EtcdBackup struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec EtcdBackupSpec `json:"spec,omitempty"`
	Status EtcdBackupStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// EtcdBackupList contém uma lista de EtcdBackup
type EtcdBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EtcdBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EtcdBackup{}, &EtcdBackupList{})
}
