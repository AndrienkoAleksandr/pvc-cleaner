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
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"path/filepath"

	"github.com/redhat-appstudio/pvc-cleaner/pkg"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/k8s"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/model"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/storage"

	pipelinev1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned/typed/pipeline/v1beta1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	CLEANUP_PVC_CONTENT_PERIOD = 3 * time.Minute
	CLEANUP_TIMEOUT            = 10 * time.Minute

	VOLUME_NAME = "source"

	CLEANER_POD_ROLE            = "pod-cleaner-role"
	CLEANER_POD_ROLEBINDING     = "pod-cleaner-rolebinding"
	CLEANER_POD_SERVICE_ACCOUNT = "pod-cleaner-service-account"
)

var isPVCSubPathCleanerRunning = false

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
		time.Sleep(CLEANUP_PVC_CONTENT_PERIOD)

		log.Println("Schedule cleanup new subpath folders content")
		if err := cleaner.cleanUpSubPathFoldersContent(); err != nil {
			log.Print(err)
		}
	}
}

func (cleaner *PVCSubPathCleaner) AddNewPVC(pipelineRun *pipelinev1.PipelineRun) {
	log.Printf("Add new pipelineRun with name %s", pipelineRun.ObjectMeta.Name)

	for _, workspace := range pipelineRun.Spec.Workspaces {
		if workspace.Name == pkg.SOURCE_WORKSPACE_NAME {
			if workspace.SubPath != "" {
				pvcSubPath := &model.PVCSubPath{PipelineRun: pipelineRun.ObjectMeta.Name, PVCSubPath: workspace.SubPath}
				cleaner.subPathStorage.AddPVCSubPath(pvcSubPath)
			}
			break
		}
	}
}

func (cleaner *PVCSubPathCleaner) CleanupSubFolders() error {
	pipelineRuns, err := cleaner.pipelineRunApi.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	if cleaner.isActivePipelineRunPresent(pipelineRuns) {
		log.Println("Stop, there are running pipelineruns")
	} else {
		log.Println("Cleanup sub-path folders")

		if err := cleaner.cleanUpSubPathFolders(); err != nil {
			return err
		}
	}

	return nil
}

func (cleaner *PVCSubPathCleaner) cleanUpSubPathFolders() error {
	log.Println("Create new pvc sub-path folder cleaner pod")

	var volumeMounts []corev1.VolumeMount
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      VOLUME_NAME,
		MountPath: pkg.SOURCE_VOLUME_DIR,
		SubPath:   ".",
	})

	if len(cleaner.subPathStorage.GetAll()) == 0 {
		log.Println("Skip pvc sub-path folder cleaner. All pvcs are clear.")
		return nil
	}

	isPVCSubPathCleanerRunning = true
	defer func() {
		isPVCSubPathCleanerRunning = false
	}()

	podName := "clean-pvc-folders-pod"
	podImage := os.Getenv("PVC_POD_CLEANER_IMAGE")
	pvcSubPathCleanerPod := cleaner.getPodCleaner(podName, podName, "/cleaner", volumeMounts, podImage)
	_, err := cleaner.clientset.CoreV1().Pods(cleaner.namespace).Create(context.TODO(), pvcSubPathCleanerPod, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	log.Print("Remove pvc sub-path folder cleaner pod")

	return cleaner.waitAndDeleteCleanUpPod(podName, "component="+podName, func(pvcSubpaths []*model.PVCSubPath) {}, []*model.PVCSubPath{})
}

func (cleaner *PVCSubPathCleaner) cleanUpSubPathFoldersContent() error {
	if isPVCSubPathCleanerRunning {
		log.Println("Skip pvc sub-path folder content cleaner, pvc sub-path folder cleaner is running.")
		return nil
	}

	pipelineRuns, err := cleaner.pipelineRunApi.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	pvcToCleanUp, err := cleaner.getPVCSubPathToContentCleanUp(pipelineRuns)
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
		pvcSubPath := filepath.Join(pkg.SOURCE_VOLUME_DIR, pvc.PVCSubPath)
		delFoldersContentCmd += "cd " + pvcSubPath + "; ls -A | xargs rm -rfv;"
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      VOLUME_NAME,
			MountPath: pvcSubPath,
			SubPath:   pvc.PVCSubPath,
		})
	}

	log.Println("Create new pvc sub-path folder content cleaner pod")

	pvcSubPathCleanerPod := cleaner.getPodCleaner("clean-pvc-sub-path-content-pod", "cleaner-pod", delFoldersContentCmd, volumeMounts, "registry.access.redhat.com/ubi8/ubi")
	_, err = cleaner.clientset.CoreV1().Pods(cleaner.namespace).Create(context.TODO(), pvcSubPathCleanerPod, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	log.Println("Remove pvc sub-path folder content cleaner pod")

	return cleaner.waitAndDeleteCleanUpPod(pvcSubPathCleanerPod.Name, "component=cleaner-pod", cleaner.deletePVCFromStorage, pvcToCleanUp)
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
				log.Printf("Pod cleaner %s succeeded", podName)
				cleanUpDone <- true
			}
			if p.Status.Phase == corev1.PodFailed {
				log.Println("Pod cleaner failed" + p.Status.Reason + " " + p.Status.Message)
				cleanUpDone <- false
			}
		}
	}(cleanUpDone)

	ticker := time.NewTicker(CLEANUP_TIMEOUT)
	select {
	case <-cleanUpDone:
		fmt.Println("[INFO] Pod cleaner finished successfully")
	case <-ticker.C:
		fmt.Println("[WARN] Remove pod cleaner due timeout")
	}
	ticker.Stop()
	watch.Stop()

	req := cleaner.clientset.CoreV1().Pods(cleaner.namespace).GetLogs(podName, &corev1.PodLogOptions{})

	podLogs, err := req.Stream(context.TODO())
	if err != nil {
		log.Fatal("error in opening stream")
	}
	defer podLogs.Close()

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, podLogs)
	if err != nil {
		log.Fatal("error in opening stream")
	}
	str := buf.String()
	log.Println("------")
	log.Print(str)
	log.Println("------")

	defer onDelete(subPaths)
	return cleaner.clientset.CoreV1().Pods(cleaner.namespace).Delete(context.TODO(), podName, metav1.DeleteOptions{})
}

func (cleaner *PVCSubPathCleaner) deletePVCFromStorage(pvcSubPaths []*model.PVCSubPath) {
	for _, pvcSubPath := range pvcSubPaths {
		cleaner.subPathStorage.Delete(pvcSubPath.PipelineRun)
	}
}

func (cleaner *PVCSubPathCleaner) getPVCSubPathToContentCleanUp(pipelineRuns *pipelinev1.PipelineRunList) ([]*model.PVCSubPath, error) {
	subPaths := cleaner.subPathStorage.GetAll()
	log.Printf("All pvc sub-path folders to filter: %d", len(subPaths))

	pvcToCleanUp := []*model.PVCSubPath{}

	for _, pvcSubPath := range subPaths {
		isPresent := false
		for _, pipelinerun := range pipelineRuns.Items {
			if pipelinerun.ObjectMeta.Name == pvcSubPath.PipelineRun {
				isPresent = true
				break
			}
		}
		if !isPresent && strings.HasPrefix(pvcSubPath.PVCSubPath, "pvc-") {
			pvcToCleanUp = append(pvcToCleanUp, pvcSubPath)
		}
	}

	return pvcToCleanUp, nil
}

func (cleaner *PVCSubPathCleaner) isActivePipelineRunPresent(pipelineRuns *pipelinev1.PipelineRunList) bool {
	for _, pipelineRun := range pipelineRuns.Items {
		if len(pipelineRun.Status.Conditions) == 0 || pipelineRun.Status.Conditions[0].Reason == "Running" {
			return true
		}
	}
	return false
}

func (cleaner *PVCSubPathCleaner) ProvidePodCleanerPermissions() error {
	// create service account if not exists
	corev1api := cleaner.clientset.CoreV1()

	_, err := corev1api.ServiceAccounts(cleaner.namespace).Get(context.TODO(), CLEANER_POD_SERVICE_ACCOUNT, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			if _, err = corev1api.ServiceAccounts(cleaner.namespace).Create(context.TODO(), cleaner.getServiceAccount(), metav1.CreateOptions{}); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	rbacApi := cleaner.clientset.RbacV1()

	// create role if not exists
	_, err = rbacApi.Roles(cleaner.namespace).Get(context.TODO(), CLEANER_POD_ROLE, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			if _, err = rbacApi.Roles(cleaner.namespace).Create(context.TODO(), cleaner.getRole(), metav1.CreateOptions{}); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	// create rolebinding if not exists
	_, err = rbacApi.RoleBindings(cleaner.namespace).Get(context.TODO(), CLEANER_POD_ROLEBINDING, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			if _, err = rbacApi.RoleBindings(cleaner.namespace).Create(context.TODO(), cleaner.getRolebinding(), metav1.CreateOptions{}); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	return nil
}

func (cleaner *PVCSubPathCleaner) getRole() *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      CLEANER_POD_ROLE,
			Namespace: cleaner.namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"tekton.dev"},
				Resources: []string{"pipelineruns", "pipelineruns/status"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
}

func (cleaner *PVCSubPathCleaner) getRolebinding() *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      CLEANER_POD_ROLEBINDING,
			Namespace: cleaner.namespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "ServiceAccount",
				Name: CLEANER_POD_SERVICE_ACCOUNT,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     CLEANER_POD_ROLE,
		},
	}
}

func (cleaner *PVCSubPathCleaner) getServiceAccount() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      CLEANER_POD_SERVICE_ACCOUNT,
			Namespace: cleaner.namespace,
		},
	}
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
			ServiceAccountName:    CLEANER_POD_SERVICE_ACCOUNT,
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
					Image:        image,
					VolumeMounts: volumeMounts,
					WorkingDir:   "/",
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: VOLUME_NAME,
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
