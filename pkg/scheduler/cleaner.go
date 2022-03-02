package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/k8s"
	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/model"
	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/storage"

	pipelinev1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned/typed/pipeline/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	DEF_PERIOD = 3 * time.Minute
	CLEANUP_TIMEOUT = 120
)

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


func (cleaner *PVCSubPathCleaner) WatchAndCleanUpEmptyFolders() {
	watch, err := cleaner.pipelineRunApi.Watch(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		for event := range watch.ResultChan() {
			p, ok := event.Object.(*pipelinev1.PipelineRun)
			if !ok {
				continue
			}

			namespace, err := k8s.GetNamespace() // todo add namespace like argument
			if err != nil {
				log.Println(err)
				continue
			}

			if len(p.Status.Conditions) == 0 {
				continue
			}
			condition := p.Status.Conditions[0]
			if condition.Reason == "Failed" || condition.Reason == "Completed" || condition.Reason == "Cancelled" {
				fmt.Println("================Status is " + condition.Reason)

				pvcToCleanUp, err := cleaner.getPVCSubPathToCleanUp()
				if err != nil {
					log.Println(err)
					continue
				}
				if len(pvcToCleanUp) == 0 {
					continue
				}

				pipelineRuns, err := cleaner.pipelineRunApi.List(context.TODO(), metav1.ListOptions{})
				if err != nil {
					log.Println(err)
					continue
				}

				stop := false
				for _, pipelineRun := range pipelineRuns.Items {
					if pipelineRun.Status.Conditions[0].Reason == "Running" {
						stop = true
						break
					}
				}
				if stop {
					log.Println("Stop, there is running pipelineruns")
					continue
				}

				var delFoldersContentCmd string
				var volumeMounts []corev1.VolumeMount
				volumeMounts = append(volumeMounts, corev1.VolumeMount{
					Name:      "source",
					MountPath: "/workspace/source",
					SubPath: ".",
				})
				for _, pvc := range pvcToCleanUp {
					delFoldersContentCmd += "rm -rf /workspace/source/" + pvc.PVCSubPath + ";"
				}

				fmt.Println("============New pod cleaner")

				podName := "clean-empty-pvc-folders-pod-" + fmt.Sprintf("%d", time.Now().UnixNano())
				pvcSubPathCleanerPod := cleaner.getPodCleaner(podName, podName, delFoldersContentCmd, volumeMounts)

				_, err = cleaner.clientset.CoreV1().Pods(namespace).Create(context.TODO(), pvcSubPathCleanerPod, metav1.CreateOptions{})
				if err != nil {
					log.Print(err)
					continue
				}

				log.Print("========= Remove empty folders cleaner pod")
				err = cleaner.waitAndDeleteCleanUpPod(namespace, podName, "component=" + podName, cleaner.deletePVCFromStorage, pvcToCleanUp)
				if err != nil {
					log.Print(err)
					continue
				}
			}
		}
	}()
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

	pvcSubPathCleanerPod := cleaner.getPodCleaner("clean-pvc-pod", "cleaner-pod", delFoldersContentCmd, volumeMounts)

	namespace, err := k8s.GetNamespace()
	if err != nil {
		return err
	}

	_, err = cleaner.clientset.CoreV1().Pods(namespace).Create(context.TODO(), pvcSubPathCleanerPod, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	return cleaner.waitAndDeleteCleanUpPod(namespace, pvcSubPathCleanerPod.Name, "component=cleaner-pod", func(pvcSubpaths []*model.PVCSubPath) {}, pvcToCleanUp)
}

func (cleaner *PVCSubPathCleaner) waitAndDeleteCleanUpPod(namespace string, podName string, label string, onDelete func([]*model.PVCSubPath), subPaths []*model.PVCSubPath) error {
	watch, err := cleaner.clientset.CoreV1().Pods(namespace).Watch(context.TODO(), metav1.ListOptions{
		LabelSelector: label,
	})
	if err != nil {
		return err
	}

	cleanUpDone := make(chan bool)
	go func(cleanUpDone chan bool) {
		for event := range watch.ResultChan() {
			p, ok := event.Object.(*corev1.Pod)
			if !ok {
				continue
			}
			if p.Status.Phase == corev1.PodSucceeded {
				log.Println("Pod cleaner succeeded")
				cleanUpDone <- true
			}
			if p.Status.Phase == corev1.PodFailed {
				log.Println("Pod cleaner failed" + p.Status.Reason + " " + p.Status.Message)
				cleanUpDone <- true
			}
		}
	}(cleanUpDone)

	ticker := time.NewTicker(CLEANUP_TIMEOUT * time.Second)
	for {
		select {
		case <- cleanUpDone:
			ticker.Stop()
			watch.Stop()
			defer onDelete(subPaths)
			return cleaner.clientset.CoreV1().Pods(namespace).Delete(context.TODO(), podName, metav1.DeleteOptions{})
		case <- ticker.C:
			ticker.Stop()
			watch.Stop()
			fmt.Println("Remove pod cleaner due timeout")
			defer onDelete(subPaths)
			return cleaner.clientset.CoreV1().Pods(namespace).Delete(context.TODO(), podName, metav1.DeleteOptions{})
		}
	}
}

func (cleaner *PVCSubPathCleaner) deletePVCFromStorage(pvcSubPaths []*model.PVCSubPath) {
	for _, pvcSubPath := range pvcSubPaths {
		if err := cleaner.subPathStorage.Delete(pvcSubPath.PipelineRun); err != nil {
			fmt.Println(err.Error())
		}
	}
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

func (cleaner *PVCSubPathCleaner) getPodCleaner(name string, label string, delFoldersContentCmd string, volumeMounts []corev1.VolumeMount) *corev1.Pod {
	deadline := int64(5400)
	labels := make(map[string]string)
	labels["component"] = label
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
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
