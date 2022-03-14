//
// Copyright 2022 Red Hat, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cleaner

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/k8s"
	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/model"
	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/storage"
	watchapi "k8s.io/apimachinery/pkg/watch"

	pipelinev1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned/typed/pipeline/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	watchTool "k8s.io/client-go/tools/watch"
)

const (
	DEF_PERIOD      = 3 * time.Minute
	CLEANUP_TIMEOUT = 600
)

type PVCSubPathCleaner struct {
	pipelineRunApi v1beta1.PipelineRunInterface
	subPathStorage *storage.PVCSubPathsStorage
	clientset      *kubernetes.Clientset
	conf           *k8s.PVCCleanerConfig
	namespace      string
}

func NewPVCSubPathCleaner(pipelineRunApi v1beta1.PipelineRunInterface, subPathStorage *storage.PVCSubPathsStorage, clientset *kubernetes.Clientset, conf *k8s.PVCCleanerConfig, namespace string) *PVCSubPathCleaner {
	return &PVCSubPathCleaner{
		pipelineRunApi: pipelineRunApi,
		subPathStorage: subPathStorage,
		clientset:      clientset,
		conf:           conf,
		namespace:      namespace,
	}
}

func (cleaner *PVCSubPathCleaner) ScheduleCleanUpSubPathFoldersContent() {
	for {
		time.Sleep(DEF_PERIOD)

		log.Println("Schedule cleanup new subpath folders content")
		if err := cleaner.deleteNotUsedSubPathsFoldersContent(); err != nil {
			log.Print(err)
		}
	}
}

func (cleaner *PVCSubPathCleaner) WatchNewPipelineRuns(storage *storage.PVCSubPathsStorage) {
	resourceVersion, err := cleaner.conf.GetWatchResourceVersion()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("=========== Resource version for watch add operation is %s", resourceVersion)

	// Watcher will be closed after some timeout, so we need to re-create watcher https://github.com/kubernetes/client-go/issues/623.
	// Let's use "NewRetryWatcher" helper for this purpose.
	retryWatcher, err := watchTool.NewRetryWatcher(resourceVersion, &cache.ListWatch{
		WatchFunc: func() func(options metav1.ListOptions) (watchapi.Interface, error) {
			return func(options metav1.ListOptions) (watchapi.Interface, error) {
				return cleaner.pipelineRunApi.Watch(context.TODO(), metav1.ListOptions{})
			}
		}(),
	})
	if err != nil {
		log.Fatal(err)
	}

	for {
		event, ok := <-retryWatcher.ResultChan()
		if !ok {
			return
		}

		if event.Type != watchapi.Added {
			continue
		}
		pipelineRun, ok := event.Object.(*pipelinev1.PipelineRun)
		if !ok {
			continue
		}
		if err = cleaner.conf.UpdateWatchResourceVersion(pipelineRun.ObjectMeta.ResourceVersion); err != nil {
			log.Println(err)
			continue
		}

		log.Printf("Add new pipelineRun with name %s", pipelineRun.ObjectMeta.Name)

		var subPath string
		for _, workspace := range pipelineRun.Spec.Workspaces {
			if workspace.Name == "workspace" {
				subPath = workspace.SubPath
				break
			}
		}

		if subPath != "" {
			pvcSubPath := &model.PVCSubPath{PipelineRun: pipelineRun.ObjectMeta.Name, PVCSubPath: subPath}
			if err := storage.AddPVCSubPath(pvcSubPath); err != nil {
				log.Println(err)
				continue
			}
		}
	}
}

func (cleaner *PVCSubPathCleaner) WatchAndCleanUpSubPathFolders() {
	// Watcher will be closed after some timeout, so we need to re-create watcher https://github.com/kubernetes/client-go/issues/623.
	// Let's use "NewRetryWatcher" helper for this purpose.
	retryWatcher, err := watchTool.NewRetryWatcher("1", &cache.ListWatch{
		WatchFunc: func() func(options metav1.ListOptions) (watchapi.Interface, error) {
			return func(options metav1.ListOptions) (watchapi.Interface, error) {
				return cleaner.pipelineRunApi.Watch(context.TODO(), metav1.ListOptions{})
			}
		}(),
	})
	if err != nil {
		log.Fatal(err)
	}

	for {
		event, ok := <-retryWatcher.ResultChan()
		if !ok {
			return
		}

		if event.Type != watchapi.Deleted {
			continue
		}
		pipelineRun, ok := event.Object.(*pipelinev1.PipelineRun)
		if !ok {
			continue
		}

		log.Println(fmt.Sprintf("=========== Event type: %v, pipelinerun: %s,amount workspaces: %d", event.Type, pipelineRun.ObjectMeta.Name, len(pipelineRun.Spec.Workspaces)))

		pipelineRuns, err := cleaner.pipelineRunApi.List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			log.Println(err)
			continue
		}

		stop := false
		for _, pipelineRun := range pipelineRuns.Items {
			if len(pipelineRun.Status.Conditions) == 0 || pipelineRun.Status.Conditions[0].Reason == "Running" {
				stop = true
				break
			}
		}
		if stop {
			log.Println("================Stop, there is running pipelineruns")
			continue
		}

		pvcToCleanUp, err := cleaner.getPVCSubPathToCleanUp()
		if err != nil {
			log.Println(err)
			continue
		}
		log.Printf("Amount pvc to cleanup is: %d", len(pvcToCleanUp))
		if len(pvcToCleanUp) == 0 {
			continue
		}

		var volumeMounts []corev1.VolumeMount
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "source",
			MountPath: "/workspace/source",
			SubPath:   ".",
		})

		log.Println("============ Create new pvc sub-path folder cleaner")

		podName := "clean-empty-pvc-folders-pod" + fmt.Sprintf("%d", time.Now().UnixNano())
		pvcSubPathCleanerPod := cleaner.getPodCleaner(podName, podName, "/cleaner", volumeMounts, "quay.io/aandriienko/pvc-pod-cleaner")
		_, err = cleaner.clientset.CoreV1().Pods(cleaner.namespace).Create(context.TODO(), pvcSubPathCleanerPod, metav1.CreateOptions{})
		if err != nil {
			log.Print(err)
			continue
		}

		log.Print("========= Remove pvc sub-path folder cleaner pod")
		err = cleaner.waitAndDeleteCleanUpPod(podName, "component="+podName, cleaner.deletePVCFromStorage, pvcToCleanUp)
		if err != nil {
			log.Print(err)
			continue
		}
	}
}

func (cleaner *PVCSubPathCleaner) deleteNotUsedSubPathsFoldersContent() error {
	pvcToCleanUp, err := cleaner.getPVCSubPathToCleanUp()
	if err != nil {
		return err
	}

	if len(pvcToCleanUp) == 0 {
		log.Println("Nothing to cleanup")
		return nil
	}

	var delFoldersContentCmd string
	var volumeMounts []corev1.VolumeMount
	for _, pvc := range pvcToCleanUp {
		delFoldersContentCmd += "cd /workspace/source/" + pvc.PVCSubPath + "; ls -A | xargs rm -rfv;"
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "source",
			MountPath: "/workspace/source/" + pvc.PVCSubPath,
			SubPath:   pvc.PVCSubPath,
		})
	}

	log.Println("--------- Create new pvc sub-path folder content cleaner pod")

	pvcSubPathCleanerPod := cleaner.getPodCleaner("clean-pvc-pod", "cleaner-pod", delFoldersContentCmd, volumeMounts, "registry.access.redhat.com/ubi8/ubi")

	_, err = cleaner.clientset.CoreV1().Pods(cleaner.namespace).Create(context.TODO(), pvcSubPathCleanerPod, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	log.Println("--------- Remove pvc sub-path folder content cleaner pod")

	return cleaner.waitAndDeleteCleanUpPod(pvcSubPathCleanerPod.Name, "component=cleaner-pod", func(pvcSubpaths []*model.PVCSubPath) {}, pvcToCleanUp)
}

func (cleaner *PVCSubPathCleaner) waitAndDeleteCleanUpPod(podName string, label string, onDelete func([]*model.PVCSubPath), subPaths []*model.PVCSubPath) error {
	watch, err := cleaner.clientset.CoreV1().Pods(cleaner.namespace).Watch(context.TODO(), metav1.ListOptions{
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
		case <-cleanUpDone:
			ticker.Stop()
			watch.Stop()
			defer onDelete(subPaths)
			return cleaner.clientset.CoreV1().Pods(cleaner.namespace).Delete(context.TODO(), podName, metav1.DeleteOptions{})
		case <-ticker.C:
			ticker.Stop()
			watch.Stop()
			fmt.Println("Remove pod cleaner due timeout")
			defer onDelete(subPaths)
			return cleaner.clientset.CoreV1().Pods(cleaner.namespace).Delete(context.TODO(), podName, metav1.DeleteOptions{})
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
	log.Printf("All pvc to filter is: %d", len(subPaths))

	pipelineRuns, err := cleaner.pipelineRunApi.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return []*model.PVCSubPath{}, err
	}
	log.Printf("getPVCSubPathToCleanUp: retrieved %d pipelineruns", len(pipelineRuns.Items))

	pvcToCleanUp := []*model.PVCSubPath{}
	for _, pvcSubPath := range subPaths {
		isPresent := false
		for _, pipelinerun := range pipelineRuns.Items {
			if pipelinerun.ObjectMeta.Name == pvcSubPath.PipelineRun {
				isPresent = true
				break
			}
		}
		if !isPresent {
			pvcToCleanUp = append(pvcToCleanUp, pvcSubPath)
		}
	}
	return pvcToCleanUp, nil
}

func (cleaner *PVCSubPathCleaner) getPodCleaner(name string, label string, delFoldersContentCmd string, volumeMounts []corev1.VolumeMount, image string) *corev1.Pod {
	deadline := int64(5400)
	labels := make(map[string]string)
	labels["component"] = label
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: "pvc-cleaner",
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
					Image: image,
					VolumeMounts: volumeMounts,
					WorkingDir:   "/",
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
