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

package controllers

import (
	"context"
	"log"

	"github.com/redhat-appstudio/pvc-cleaner/pkg"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/cleaner"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/k8s"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/storage"
	pipelinev1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	v1beta1 "github.com/tektoncd/pipeline/pkg/client/clientset/versioned/typed/pipeline/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	watchapi "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	watchTool "k8s.io/client-go/tools/watch"

	"fmt"
)

type CleanupPVCController struct {
	// pipelineRunApi with "all-namespaces" scope
	pipelineRunApi v1beta1.PipelineRunInterface
	conf           *k8s.PVCCleanerConfig
	clientset      *kubernetes.Clientset
	tknClientset   *versioned.Clientset

	namespacedCleaners map[string]*cleaner.PVCSubPathCleaner
}

func NewCleanupPVCController(
	pipelineRunApi v1beta1.PipelineRunInterface,
	conf *k8s.PVCCleanerConfig,
	clientset *kubernetes.Clientset,
	tknClientset *versioned.Clientset) *CleanupPVCController {
	return &CleanupPVCController{
		pipelineRunApi:     pipelineRunApi,
		conf:               conf,
		clientset:          clientset,
		tknClientset:       tknClientset,
		namespacedCleaners: make(map[string]*cleaner.PVCSubPathCleaner),
	}
}

func (controller *CleanupPVCController) Start() {
	resourceVersion, err := controller.conf.GetWatchResourceVersion()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Resource version for watch operations is %s", resourceVersion)

	// Watcher will be closed after some timeout, so we need to re-create watcher https://github.com/kubernetes/client-go/issues/623.
	// Let's use "NewRetryWatcher" helper for this purpose.
	retryWatcher, err := watchTool.NewRetryWatcher(resourceVersion, &cache.ListWatch{
		WatchFunc: func() func(options metav1.ListOptions) (watchapi.Interface, error) {
			return func(options metav1.ListOptions) (watchapi.Interface, error) {
				return controller.pipelineRunApi.Watch(context.TODO(), metav1.ListOptions{})
			}
		}(),
	})
	if err != nil {
		log.Fatal(err)
	}

	for {
		event, ok := <-retryWatcher.ResultChan()
		if !ok {
			log.Printf("Something went wrong with watcher...")
			return
		}

		pipelineRun, ok := event.Object.(*pipelinev1.PipelineRun)
		if !ok {
			continue
		}

		if event.Type == watchapi.Added {
			if err := controller.addFinalizer(pipelineRun); err != nil {
				log.Println(err)
			}
			log.Println(fmt.Sprintf("Event type: %v, pipelinerun: %s,amount workspaces: %d", event.Type, pipelineRun.ObjectMeta.Name, len(pipelineRun.Spec.Workspaces)))
			if err := controller.onCreatePipelineRun(pipelineRun); err != nil {
				log.Println(err)
				continue
			}
		}

		if event.Type == watchapi.Modified && !pipelineRun.DeletionTimestamp.IsZero() {
			log.Println(fmt.Sprintf("Event type: %v, pipelinerun: %s,amount workspaces: %d", event.Type, pipelineRun.ObjectMeta.Name, len(pipelineRun.Spec.Workspaces)))
			if err := controller.onDeletePipelineRun(pipelineRun.ObjectMeta.Namespace); err != nil {
				log.Println(err)
				continue
			}
		}
	}
}

func (controller *CleanupPVCController) onCreatePipelineRun(pipelineRun *pipelinev1.PipelineRun) error {
	// Update initial  pipelinerun resource version to prevent send old "add" events after application pod restart.
	if err := controller.conf.UpdateWatchResourceVersion(pipelineRun.ObjectMeta.ResourceVersion); err != nil {
		return err
	}

	namespace := pipelineRun.ObjectMeta.Namespace
	pvcCleaner := controller.namespacedCleaners[namespace]
	if pvcCleaner == nil {
		log.Printf("Create pvc cleaner for namespace %s", namespace)
		// Create pipelineRunApi single namespaced mode
		pipelineRunApi := controller.tknClientset.TektonV1beta1().PipelineRuns(namespace)
		pvcCleaner = cleaner.NewPVCSubPathCleaner(
			pipelineRunApi,
			storage.NewPVCSubPathsStorage(),
			controller.clientset,
			k8s.NewCleanerConfig(controller.clientset, namespace),
			namespace,
		)
		if err := pvcCleaner.ProvidePodCleanerPermissions(); err != nil {
			log.Println(err.Error())
		}
		go pvcCleaner.ScheduleCleanUpSubPathFoldersContent()
		controller.namespacedCleaners[namespace] = pvcCleaner
	}

	log.Printf("Add new pvc for pipelinerun %s in namespace %s", pipelineRun.ObjectMeta.Name, pipelineRun.ObjectMeta.Namespace)
	pvcCleaner.AddNewPVC(pipelineRun)

	return nil
}

func (controller *CleanupPVCController) onDeletePipelineRun(namespaceName string) error {
	cleaner := controller.namespacedCleaners[namespaceName]
	if cleaner == nil {
		return nil
	}

	namespaceInDeletionState, err := pkg.IsNamespaceInDeletingState(controller.clientset, namespaceName)
	if err != nil {
		return err
	}
	if namespaceInDeletionState {
		delete(controller.namespacedCleaners, namespaceName)
		return nil
	}

	go cleaner.CleanupSubFolders()

	return nil
}

func (cc *CleanupPVCController) addFinalizer(pipelineRun *pipelinev1.PipelineRun) error {
	api := cc.tknClientset.TektonV1beta1().PipelineRuns(pipelineRun.GetNamespace())

	patchTemplate := `{"metadata": {"finalizers": ["%s"]}}`
	patch := fmt.Sprintf(patchTemplate, pkg.PIPELINERUN_FINALIZER_NAME)

	if _, err := api.Patch(context.TODO(), pipelineRun.GetName(), types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return err
	}

	return nil
}
