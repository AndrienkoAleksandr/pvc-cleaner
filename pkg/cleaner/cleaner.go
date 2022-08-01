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
	"log"
	"os"
	"time"

	"github.com/redhat-appstudio/pvc-cleaner/pkg"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/model"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/storage"

	"sync"

	pipelinev1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned/typed/pipeline/v1beta1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
	typedrbacv1 "k8s.io/client-go/kubernetes/typed/rbac/v1"
)

const (
	CLEANUP_PVC_CONTENT_PERIOD = 3 * time.Minute
	CLEANUP_TIMEOUT            = 10 * time.Minute

	PVC_CLEANER_POD_CLUSTER_ROLE    = "pvc-cleaner-pod-cluster-role"
	PVC_CLEANER_POD_ROLEBINDING     = "pvc-cleaner-pod-rolebinding"
	PVC_CLEANER_POD_SERVICE_ACCOUNT = "pvc-cleaner-pod-service-account"

	SUB_PATH_CONTENT_CLEANER_POD = "clean-pvc-sub-path-content-pod"

	DEFAULT_PVC_CLAIM_NAME = "app-studio-default-workspace"
)

var isPVCSubPathCleanerRunning = false

type PVCSubPathCleaner struct {
	pipelineRunApi v1beta1.PipelineRunInterface
	subPathStorage *storage.PVCSubPathsStorage
	corev1         v1.CoreV1Interface
	rbacv1         typedrbacv1.RbacV1Interface
	namespace      string

	delPVCFoldersMu sync.Mutex
}

func NewPVCSubPathCleaner(
	pipelineRunApi v1beta1.PipelineRunInterface,
	subPathStorage *storage.PVCSubPathsStorage,
	corev1 v1.CoreV1Interface,
	rbacv1 typedrbacv1.RbacV1Interface,
	namespace string) *PVCSubPathCleaner {
	return &PVCSubPathCleaner{
		pipelineRunApi: pipelineRunApi,
		subPathStorage: subPathStorage,
		corev1:         corev1,
		rbacv1:         rbacv1,
		namespace:      namespace,
	}
}

func (cleaner *PVCSubPathCleaner) ScheduleCleanUpSubPathFoldersContent() {
	ticker := time.NewTicker(CLEANUP_PVC_CONTENT_PERIOD)
	for {
		<-ticker.C
		log.Printf("Schedule cleanup new subpath folders content for \"%s\" namespace", cleaner.namespace)

		isNamespaceInDeletingState, err := pkg.IsNamespaceInDeletingState(cleaner.corev1, cleaner.namespace)
		if err != nil {
			log.Println(err)
			continue
		}
		if isNamespaceInDeletingState {
			log.Printf("PVC cleaner completed work for namespace \"%s\"", cleaner.namespace)
			break
		}

		if isPVCSubPathCleanerRunning {
			log.Printf("Skip pvc sub-path folder content cleaner, pvc sub-path folder cleaner is running in namespace \"%s\".", cleaner.namespace)
			continue
		}

		notEmptyPVCs, err := cleaner.createPodToCleanUpSubPathFoldersContent()
		if err != nil {
			log.Printf("Failed to create pod to clean up sub path folders content in the namespace \"%s\". Cause: %s", cleaner.namespace, err.Error())
			continue
		}

		log.Printf("Remove pvc sub-path folder content cleaner pod in namespace \"%s\"", cleaner.namespace)
	    if err := cleaner.waitAndDeleteCleanUpPod(SUB_PATH_CONTENT_CLEANER_POD, "component=cleaner-pod", cleaner.markCleanPVCContent, notEmptyPVCs); err != nil {
			log.Printf("Failed to delete pvc sub-path folder content cleaner pod in the namespace \"%s\". Cause: %s", cleaner.namespace, err.Error())
			continue
		}
	}
}

func (cleaner *PVCSubPathCleaner) AddNewPVC(pipelineRun *pipelinev1.PipelineRun) {
	log.Printf("Add new pipelineRun with name %s in namespace \"%s\"", pipelineRun.ObjectMeta.Name, cleaner.namespace)

	for _, workspace := range pipelineRun.Spec.Workspaces {
		if workspace.Name == pkg.SOURCE_WORKSPACE_NAME {
			if workspace.PersistentVolumeClaim == nil {
				log.Printf("Skip to track pipelinerun %s. PVC claim was not defined in the pipelinerun", pipelineRun.Name)
				return
			}
			claimName := workspace.PersistentVolumeClaim.ClaimName
			if claimName != pkg.APPSTUDIO_SERVICES_PVC && claimName != pkg.DEFAULT_WORKSPACE_PVC {
				log.Printf("Skip to track pipelinerun %s. Unknown PVC claim name %s. Supported values are: %s,%s", pipelineRun.Name, claimName, pkg.APPSTUDIO_SERVICES_PVC, pkg.DEFAULT_WORKSPACE_PVC)
				return
			}
			if workspace.SubPath != "" {
				pvcSubPath := &model.PVCSubPath{
					PipelineRun:  pipelineRun.ObjectMeta.Name,
					PVCSubPath:   workspace.SubPath,
					PVCClaimName: claimName,
				}
				log.Printf("Add pvc %+v", pvcSubPath)
				cleaner.subPathStorage.AddPVCSubPath(pvcSubPath)
			}
			break
		}
	}
}

func (cleaner *PVCSubPathCleaner) CleanupSubFolders() {
	cleaner.delPVCFoldersMu.Lock()
	defer cleaner.delPVCFoldersMu.Unlock()

	pipelineRuns, err := cleaner.pipelineRunApi.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Println(err.Error())
		return
	}

	if cleaner.isActivePipelineRunPresent(pipelineRuns) {
		log.Printf("Stop, there are running pipelineruns in namespace \"%s\"", cleaner.namespace)
	} else {
		log.Printf("Cleanup pvc sub-path folders in namespace \"%s\"", cleaner.namespace)
		pod, pvcToCleanUp, err := cleaner.createPodToCleanUpSubPathFolders()
		if err != nil {
			log.Printf("Failed to create pod to clean up sub-path folders in the namespace \"%s\". Cause: %s", cleaner.namespace, err.Error())
			return
		}

		if pod == nil {
			return
		}

		log.Printf("Remove pvc sub-path folders cleaner pod in namespace \"%s\"", cleaner.namespace)
	    if cleaner.waitAndDeleteCleanUpPod(pod.Name, "component="+pod.Name, cleaner.deletePVCFromStorage, pvcToCleanUp); err != nil {
			log.Printf("Failed to delete pvc sub-path folders cleaner pod in the namespace \"%s\". Cause: %s", cleaner.namespace, err.Error())
			return
		}
	}
}

func (cleaner *PVCSubPathCleaner) createPodToCleanUpSubPathFolders() (*corev1.Pod, []*model.PVCSubPath, error) {
	log.Printf("Create new pvc sub-path folder cleaner pod in namespace \"%s\"", cleaner.namespace)

	pvcToCleanUps, err := cleaner.getPVCSubPathToCleanUp()
	if err != nil {
		return nil, nil, err
	}

	if len(pvcToCleanUps) == 0 {
		log.Printf("Skip pvc sub-path folder cleaner. All required folders were removed. Namespace: %s.", cleaner.namespace)
		return nil, nil, nil
	}

	command := "/cleaner"
	volumeMounts := []corev1.VolumeMount{}
	volumes := []corev1.Volume{}
	var isAppServicePVCPresent, isDefWorkspacePVCPresent bool
	for _, pvcToCleanUp := range pvcToCleanUps {
		command += " --pvc-subpaths=" + "/" + pvcToCleanUp.PVCClaimName + "/" + pvcToCleanUp.PVCSubPath

		if pvcToCleanUp.PVCClaimName == pkg.APPSTUDIO_SERVICES_PVC {
			isAppServicePVCPresent = true
		}
		if pvcToCleanUp.PVCClaimName == pkg.DEFAULT_WORKSPACE_PVC {
			isDefWorkspacePVCPresent = true
		}
	}

	if isAppServicePVCPresent {
		if err := cleaner.getPVCOrDie(pkg.APPSTUDIO_SERVICES_PVC); err != nil {
			return nil, nil, err
		}
		volumes = append(volumes, getVolume(pkg.APPSTUDIO_SERVICES_PVC))
		volumeMounts = append(volumeMounts, getVolumeMount(pkg.APPSTUDIO_SERVICES_PVC))
	}
	if isDefWorkspacePVCPresent {
		if err := cleaner.getPVCOrDie(pkg.DEFAULT_WORKSPACE_PVC); err != nil {
			return nil, nil, err
		}
		volumes = append(volumes, getVolume(pkg.DEFAULT_WORKSPACE_PVC))
		volumeMounts = append(volumeMounts, getVolumeMount(pkg.DEFAULT_WORKSPACE_PVC))
	}

	isPVCSubPathCleanerRunning = true
	defer func() {
		isPVCSubPathCleanerRunning = false
	}()

	podName := "clean-pvc-folders-pod"
	podImage := os.Getenv("PVC_POD_CLEANER_IMAGE")
	pvcSubPathCleanerPod := cleaner.getPodCleaner(podName, podName, command, volumes, volumeMounts, podImage)
	pod, err := cleaner.corev1.Pods(cleaner.namespace).Create(context.TODO(), pvcSubPathCleanerPod, metav1.CreateOptions{})
	return pod, pvcToCleanUps, err
}

func (cleaner *PVCSubPathCleaner) createPodToCleanUpSubPathFoldersContent() ([]*model.PVCSubPath, error) {
	notEmptyPVCs, err := cleaner.getNotEmptyPVCs()
	if err != nil {
		return nil, err
	}
	if len(notEmptyPVCs) == 0 {
		log.Printf("Nothing to cleanup. Folders content was removed. namespace \"%s\"", cleaner.namespace)
		return nil, nil
	}

	var delFoldersContentCmd string
	var volumeMounts []corev1.VolumeMount
	var volumes []corev1.Volume
	var isAppServicePVCPresent, isDefWorkspacePVCPresent bool
	for _, pvc := range notEmptyPVCs {
		subPath := "/" + pvc.PVCClaimName + "/" + pvc.PVCSubPath
		delFoldersContentCmd += " cd " + subPath + " && ls -A | xargs rm -rfv || true;"

		if pvc.PVCClaimName == pkg.APPSTUDIO_SERVICES_PVC {
			isAppServicePVCPresent = true
		}
		if pvc.PVCClaimName == pkg.DEFAULT_WORKSPACE_PVC {
			isDefWorkspacePVCPresent = true
		}
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      pvc.PVCClaimName,
			MountPath: subPath,
			SubPath:   pvc.PVCSubPath,
		})
	}

	if isAppServicePVCPresent {
		if err := cleaner.getPVCOrDie(pkg.APPSTUDIO_SERVICES_PVC); err != nil {
			return nil, err
		}
		volumes = append(volumes, getVolume(pkg.APPSTUDIO_SERVICES_PVC))
	}
	if isDefWorkspacePVCPresent {
		if err := cleaner.getPVCOrDie(pkg.DEFAULT_WORKSPACE_PVC); err != nil {
			return nil, err
		}
		volumes = append(volumes, getVolume(pkg.DEFAULT_WORKSPACE_PVC))
	}

	log.Printf("Create new pvc sub-path folder content cleaner pod in namespace \"%s\"", cleaner.namespace)

	pvcSubPathCleanerPod := cleaner.getPodCleaner(SUB_PATH_CONTENT_CLEANER_POD, "cleaner-pod", delFoldersContentCmd, volumes, volumeMounts, "registry.access.redhat.com/ubi8/ubi")
	_, err = cleaner.corev1.Pods(cleaner.namespace).Create(context.TODO(), pvcSubPathCleanerPod, metav1.CreateOptions{})
	return notEmptyPVCs, err
}

func (cleaner *PVCSubPathCleaner) waitAndDeleteCleanUpPod(podName string, label string, onDelete func([]*model.PVCSubPath), subPaths []*model.PVCSubPath) error {
	watch, err := cleaner.corev1.Pods(cleaner.namespace).Watch(context.TODO(), metav1.ListOptions{
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
				log.Printf("Pod cleaner failed in namespace \"%s\". Reason: %s. Error message: %s.", cleaner.namespace, p.Status.Reason, p.Status.Message)
				cleanUpDone <- false
			}
		}
	}(cleanUpDone)

	ticker := time.NewTicker(CLEANUP_TIMEOUT)
	select {
	case <-cleanUpDone:
		log.Printf("[INFO] Pod cleaner finished successfully in namespace \"%s\"", cleaner.namespace)
	case <-ticker.C:
		log.Printf("[WARN] Remove pod cleaner due timeout in namespace \"%s\"", cleaner.namespace)
	}
	ticker.Stop()
	watch.Stop()

	defer onDelete(subPaths)
	return cleaner.corev1.Pods(cleaner.namespace).Delete(context.TODO(), podName, metav1.DeleteOptions{})
}

func (cleaner *PVCSubPathCleaner) deletePVCFromStorage(pvcSubPaths []*model.PVCSubPath) {
	for _, pvcSubPath := range pvcSubPaths {
		cleaner.subPathStorage.Delete(pvcSubPath.PipelineRun)
	}
}

func (cleaner *PVCSubPathCleaner) markCleanPVCContent(pvcSubPaths []*model.PVCSubPath) {
	for _, pvcSubPath := range pvcSubPaths {
		pvcSubPath.IsContentPruned = true
		cleaner.subPathStorage.Update(pvcSubPath)
	}
}

func (cleaner *PVCSubPathCleaner) getNotEmptyPVCs() ([]*model.PVCSubPath, error) {
	notEmptyPVCs := []*model.PVCSubPath{}
	pvcToCleanUp, err := cleaner.getPVCSubPathToCleanUp()
	if err != nil {
		return notEmptyPVCs, err
	}

	for _, pvcSubPath := range pvcToCleanUp {
		if !pvcSubPath.IsContentPruned {
			notEmptyPVCs = append(notEmptyPVCs, pvcSubPath)
		}
	}
	return notEmptyPVCs, nil
}

func (cleaner *PVCSubPathCleaner) getPVCSubPathToCleanUp() ([]*model.PVCSubPath, error) {
	subPaths := cleaner.subPathStorage.GetAll()
	log.Printf("All pvc sub-path folders to filter: %d in namespace \"%s\"", len(subPaths), cleaner.namespace)
	pvcToCleanUp := []*model.PVCSubPath{}

	if len(subPaths) == 0 {
		return pvcToCleanUp, nil
	}

	pipelineRuns, err := cleaner.pipelineRunApi.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return pvcToCleanUp, err
	}

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

func (cleaner *PVCSubPathCleaner) isActivePipelineRunPresent(pipelineRuns *pipelinev1.PipelineRunList) bool {
	for _, pipelineRun := range pipelineRuns.Items {
		if len(pipelineRun.Status.Conditions) == 0 ||
			(pipelineRun.Status.Conditions[0].Reason == "Running" && pipelineRun.DeletionTimestamp.IsZero()) {
			return true
		}
	}
	return false
}

func (cleaner *PVCSubPathCleaner) ProvidePodCleanerPermissions() error {
	// create service account if not exists
	_, err := cleaner.corev1.ServiceAccounts(cleaner.namespace).Get(context.TODO(), PVC_CLEANER_POD_SERVICE_ACCOUNT, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			if _, err = cleaner.corev1.ServiceAccounts(cleaner.namespace).Create(context.TODO(), cleaner.getServiceAccount(), metav1.CreateOptions{}); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	// create rolebinding if not exists
	_, err = cleaner.rbacv1.RoleBindings(cleaner.namespace).Get(context.TODO(), PVC_CLEANER_POD_ROLEBINDING, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			if _, err = cleaner.rbacv1.RoleBindings(cleaner.namespace).Create(context.TODO(), cleaner.getRolebinding(), metav1.CreateOptions{}); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	return nil
}

func (cleaner *PVCSubPathCleaner) getRolebinding() *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PVC_CLEANER_POD_ROLEBINDING,
			Namespace: cleaner.namespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "ServiceAccount",
				Name: PVC_CLEANER_POD_SERVICE_ACCOUNT,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     PVC_CLEANER_POD_CLUSTER_ROLE,
		},
	}
}

func (cleaner *PVCSubPathCleaner) getServiceAccount() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PVC_CLEANER_POD_SERVICE_ACCOUNT,
			Namespace: cleaner.namespace,
		},
	}
}

func (cleaner *PVCSubPathCleaner) getPodCleaner(name string, label string, delFoldersContentCmd string, volumes []corev1.Volume, volumeMounts []corev1.VolumeMount, image string) *corev1.Pod {
	deadline := int64(5400)
	labels := make(map[string]string)
	labels["component"] = label
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: corev1.PodSpec{
			ServiceAccountName:    PVC_CLEANER_POD_SERVICE_ACCOUNT,
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
			Volumes: volumes,
		},
	}
}

func (cleaner *PVCSubPathCleaner) getPVCOrDie(pvcClaim string) error {
	if _, err := cleaner.corev1.PersistentVolumeClaims(cleaner.namespace).Get(context.TODO(), pvcClaim, metav1.GetOptions{}); err != nil {
		return err
	}
	return nil
}

func getVolume(pvcClaimName string) corev1.Volume {
	return corev1.Volume{
		Name: pvcClaimName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcClaimName,
			},
		},
	}
}

func getVolumeMount(pvcClaimName string) corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      pvcClaimName,
		MountPath: "/" + pvcClaimName,
		SubPath:   ".",
	}
}
