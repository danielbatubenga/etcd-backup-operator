package controller

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupv1 "github.com/danielbatubenga/etcd-backup-operator/api/v1"
)

// EtcdBackupReconciler reconcilia o recurso EtcdBackup
type EtcdBackupReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=backup.example.com,resources=etcdbackups,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=backup.example.com,resources=etcdbackups/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=backup.example.com,resources=etcdbackups/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=create;get;list;watch;delete
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

// Reconcile é chamado sempre que um recurso EtcdBackup é criado/alterado
func (r *EtcdBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Tenta buscar como EtcdBackup
	var etcdBackup backupv1.EtcdBackup
	err := r.Get(ctx, req.NamespacedName, &etcdBackup)
	if err == nil {
		// É um EtcdBackup - tratar criação/atualização
		return r.reconcileBackup(ctx, &etcdBackup)
	}
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}

	// Não é EtcdBackup, pode ser um Job (Owns)
	var job batchv1.Job
	if err := r.Get(ctx, req.NamespacedName, &job); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Encontra o EtcdBackup owner
	for _, owner := range job.OwnerReferences {
		if owner.Kind == "EtcdBackup" && owner.APIVersion == "backup.example.com/v1" {
			backupName := types.NamespacedName{
				Namespace: job.Namespace,
				Name:      owner.Name,
			}
			if err := r.Get(ctx, backupName, &etcdBackup); err != nil {
				return ctrl.Result{}, err
			}
			return r.reconcileJob(ctx, &etcdBackup, &job)
		}
	}

	return ctrl.Result{}, nil
}

func (r *EtcdBackupReconciler) reconcileBackup(ctx context.Context, etcdBackup *backupv1.EtcdBackup) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Se já foi concluído (CompletionTime preenchido), não faz nada
	if etcdBackup.Status.CompletionTime != nil {
		return ctrl.Result{}, nil
	}

	// Se já iniciou, tenta localizar um Job associado e atualizar status
	if etcdBackup.Status.StartTime != nil {
		var jobs batchv1.JobList
		if err := r.List(ctx, &jobs,
			client.InNamespace(etcdBackup.Namespace),
			client.MatchingLabels{"etcdbackup.backup.example.com/name": etcdBackup.Name},
		); err != nil {
			return ctrl.Result{}, err
		}
		for i := range jobs.Items {
			job := &jobs.Items[i]
			if job.DeletionTimestamp != nil {
				continue
			}
			return r.reconcileJob(ctx, etcdBackup, job)
		}
		return ctrl.Result{}, nil
	}

	// 1. Descobrir em qual nó o etcd está rodando (se não foi especificado)
	nodeName := etcdBackup.Spec.NodeName
	if nodeName == "" {
		podList := &corev1.PodList{}
		err := r.List(ctx, podList,
			client.InNamespace("kube-system"),
			client.MatchingLabels{"component": "etcd"})
		if err != nil {
			log.Error(err, "falha ao listar pods do etcd")
			return ctrl.Result{}, err
		}
		if len(podList.Items) == 0 {
			log.Info("nenhum pod etcd encontrado, aguardando...")
			// Requeue após 30 segundos
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		nodeName = podList.Items[0].Spec.NodeName
		log.Info("nó do etcd detectado", "node", nodeName)
	}

	// 2. Definir caminho de destino (padrão /home)
	destPath := etcdBackup.Spec.DestinationPath
	if destPath == "" {
		destPath = "/home"
	}

	// 3. Gerar nome único para o Job
	jobName := fmt.Sprintf("etcd-backup-%s-%s", etcdBackup.Name, randString(5))

	// 4. Construir o Job
	hostPathTypeDir := corev1.HostPathDirectory
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: etcdBackup.Namespace, // usamos o mesmo namespace do CR
			Labels: map[string]string{
				"etcdbackup.backup.example.com/name": etcdBackup.Name,
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{"kubernetes.io/hostname": nodeName},
					Volumes: []corev1.Volume{
						{
							Name: "etcd-certs",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/etc/kubernetes/pki/etcd",
									Type: &hostPathTypeDir,
								},
							},
						},
						{
							Name: "host-home",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: destPath,
									Type: &hostPathTypeDir,
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "backup",
							Image:   "alpine:latest", // imagem leve, instalaremos etcdctl e zip
							Command: []string{"/bin/sh"},
							Args: []string{"-c", `
								# Instala etcdctl e zip (imagem Alpine)
								apk add --no-cache etcdctl zip

								# Define variáveis de ambiente para etcdctl
								export ETCDCTL_ENDPOINTS=https://127.0.0.1:2379
								export ETCDCTL_CACERT=/etc/kubernetes/pki/etcd/ca.crt
								export ETCDCTL_CERT=/etc/kubernetes/pki/etcd/server.crt
								export ETCDCTL_KEY=/etc/kubernetes/pki/etcd/server.key

								# Nome do snapshot com timestamp
								TIMESTAMP=$(date +%Y%m%d-%H%M%S)
								SNAPSHOT_FILE=/tmp/etcd-snapshot-${TIMESTAMP}.db
								ZIP_FILE=etcd-backup-${TIMESTAMP}.zip

								# Executa o snapshot
								etcdctl snapshot save $SNAPSHOT_FILE

								# Compacta em zip
								cd /tmp
								zip $ZIP_FILE $(basename $SNAPSHOT_FILE)

								# Copia para o diretório montado (hostPath)
								cp $ZIP_FILE /home/

								echo "Backup concluído: /home/$ZIP_FILE"
							`},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "etcd-certs", MountPath: "/etc/kubernetes/pki/etcd", ReadOnly: true},
								{Name: "host-home", MountPath: "/home"},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			},
		},
	}

	// Define o owner reference para que o Job seja deletado automaticamente quando o CR for deletado
	if err := ctrl.SetControllerReference(etcdBackup, job, r.Scheme); err != nil {
		log.Error(err, "falha ao definir owner reference")
		return ctrl.Result{}, err
	}

	// 5. Cria o Job no cluster
	log.Info("criando job de backup", "job", jobName, "node", nodeName)
	if err := r.Create(ctx, job); err != nil {
		log.Error(err, "falha ao criar job")
		return ctrl.Result{}, err
	}

	// 6. Atualiza o status do EtcdBackup
	now := metav1.Now()
	etcdBackup.Status.StartTime = &now
	etcdBackup.Status.Conditions = []metav1.Condition{
		{
			Type:               "Started",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "JobCreated",
			Message:            fmt.Sprintf("Job %s criado no nó %s", jobName, nodeName),
		},
	}
	if err := r.Status().Update(ctx, etcdBackup); err != nil {
		log.Error(err, "falha ao atualizar status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *EtcdBackupReconciler) reconcileJob(ctx context.Context, etcdBackup *backupv1.EtcdBackup, job *batchv1.Job) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	if etcdBackup.Status.CompletionTime != nil {
		return ctrl.Result{}, nil
	}

	// Ainda em execução
	if job.Status.Succeeded == 0 && job.Status.Failed == 0 {
		return ctrl.Result{}, nil
	}

	now := metav1.Now()
	completion := &now
	if job.Status.CompletionTime != nil {
		completion = job.Status.CompletionTime
	}

	if job.Status.Succeeded > 0 {
		etcdBackup.Status.CompletionTime = completion
		etcdBackup.Status.Conditions = []metav1.Condition{
			{
				Type:               "Completed",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: *completion,
				Reason:             "JobSucceeded",
				Message:            fmt.Sprintf("Job %s concluiu com sucesso", job.Name),
			},
		}
	} else if job.Status.Failed > 0 {
		etcdBackup.Status.CompletionTime = completion
		etcdBackup.Status.Conditions = []metav1.Condition{
			{
				Type:               "Failed",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: *completion,
				Reason:             "JobFailed",
				Message:            fmt.Sprintf("Job %s falhou", job.Name),
			},
		}
	}

	if err := r.Status().Update(ctx, etcdBackup); err != nil {
		log.Error(err, "falha ao atualizar status do EtcdBackup a partir do Job", "job", job.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager configura o controller com o Manager
func (r *EtcdBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupv1.EtcdBackup{}).
		Owns(&batchv1.Job{}). // importante: reagir a mudanças nos Jobs que criamos
		Complete(r)
}

// randString gera uma string aleatória de tamanho n (para nomes de Job)
func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}