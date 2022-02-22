package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/model"
	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/storage"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned/typed/pipeline/v1beta1"
	"k8s.io/client-go/kubernetes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	corev1 "k8s.io/api/core/v1"
)

const DEF_PERIOD = 3 * time.Minute

type PVCSubPathCleaner struct {
	pipelineRunApi v1beta1.PipelineRunInterface
	subPathStorage *storage.PVCSubPathsStorage
	clientset *kubernetes.Clientset
}

func NewPVCSubPathCleaner(pipelineRunApi v1beta1.PipelineRunInterface, subPathStorage *storage.PVCSubPathsStorage, clientset *kubernetes.Clientset) *PVCSubPathCleaner {
	return &PVCSubPathCleaner{
		pipelineRunApi: pipelineRunApi, 
		subPathStorage: subPathStorage,
		clientset: clientset,
	}
}

func (cleaner *PVCSubPathCleaner) Schedule() {
	for ;; {
		time.Sleep(DEF_PERIOD)

		if err := cleaner.DeleteNotUsedSubPaths(); err != nil {
			log.Print(err)
		}
	}
}

func (cleaner *PVCSubPathCleaner) DeleteNotUsedSubPaths() error {
	pvcToCleanUp, err := cleaner.getPVCSubPathToCleanUp()
	if err != nil {
		return err
	}

	if len(pvcToCleanUp) == 0 {
		fmt.Println("Nothing to cleanup")
		return nil
	}

	var delFoldersContentCmd string
	var volumeMounts []corev1.VolumeMount
	for _, pvc := range pvcToCleanUp {
		delFoldersContentCmd += "cd /workspace/source/" + pvc.PVCSubPath + "; ls -A | xargs rm -rfv;"
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "source",
			MountPath: "/workspace/source/" + pvc.PVCSubPath,
			SubPath: pvc.PVCSubPath,
		})
	}

	pvcSubPathCleanerPod := cleaner.getPodCleaner(delFoldersContentCmd, volumeMounts)
	// todo set up namespace
	_, err = cleaner.clientset.CoreV1().Pods("default").Create(context.TODO(), pvcSubPathCleanerPod, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	// todo delete pvc cleaner pod....

	return nil
}

func (cleaner *PVCSubPathCleaner) getPVCSubPathToCleanUp() ([]*model.PVCSubPath, error) {
	subPaths, err := cleaner.subPathStorage.GetAll()
	if err != nil {
		return []*model.PVCSubPath{}, err
	}

	pvcToCleanUp := []*model.PVCSubPath{}
	for _, pvcSubPath := range subPaths {
		_, err := cleaner.pipelineRunApi.Get(context.TODO(), pvcSubPath.PipelineRun, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			pvcToCleanUp = append(pvcToCleanUp, pvcSubPath)
		}
	}
	return pvcToCleanUp, nil
}

func (cleaner *PVCSubPathCleaner) getPodCleaner(delFoldersContentCmd string, volumeMounts []corev1.VolumeMount) *corev1.Pod {
	deadline := int64(5400)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "clean-pvc-pod",
			Namespace: "default", // todo
		},
		Spec: corev1.PodSpec{
			RestartPolicy:         "Never",
			ActiveDeadlineSeconds: &deadline,
			Containers: []corev1.Container{
				{
					Name: "pvc-cleaner",
					Command: []string{
						"/bin/bash",
					},
					TTY: true,
					Args: []string{
						"-c",
						delFoldersContentCmd,
					},
					Image: "registry.access.redhat.com/ubi8/ubi",
					VolumeMounts: volumeMounts,
					WorkingDir: "/",
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "source",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: "app-studio-default-workspace",
						},
					},
				},
			},
		},
	}
}
