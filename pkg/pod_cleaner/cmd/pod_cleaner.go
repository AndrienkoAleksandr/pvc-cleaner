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

package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/redhat-appstudio/pvc-cleaner/pkg"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/k8s"
	pipelinev1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned/typed/pipeline/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	watchapi "k8s.io/apimachinery/pkg/watch"
)

func main() {
	pkg.ParseFlags()

	config := k8s.GetClusterConfig()

	log.Println("Create config")

	// create Tekton clientset
	tknClientset, err := versioned.NewForConfig(config)
	if err != nil {
		log.Fatalf("failed to create pipeline clientset %s", err)
	}

	namespace, err := k8s.GetNamespace()
	if err != nil {
		log.Fatalf("failed to create pipeline clientset %s", err)
	}
	pipelineRunApi := tknClientset.TektonV1beta1().PipelineRuns(namespace)

	pipelineRuns, err := pipelineRunApi.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Watch new pipelineruns...")
	go watchNewPipelineRuns(pipelineRunApi, pipelineRuns.ResourceVersion)

	var wg sync.WaitGroup
	for _, pipelinerun := range pipelineRuns.Items {
		if !pipelinerun.DeletionTimestamp.IsZero() {
			var pvcSubPath string
			for _, workspace := range pipelinerun.Spec.Workspaces {
				if workspace.Name == "workspace" {
					pvcSubPath = workspace.SubPath
				}
			}

			if pvcSubPath == "" {
				log.Printf("Skip pvc cleanup. Coresponding workspace was not found.")
				continue
			}

			wg.Add(1)

			log.Printf("Cleanup subpath %s for pipelinerun %s", pvcSubPath, pipelinerun.Name)
			go func(pvcSubPath string, wg *sync.WaitGroup, pipelineRunName string, pipelineRunApi v1beta1.PipelineRunInterface) {
				defer wg.Done()
				cleanUpSubpaths(pvcSubPath, wg)
				removePipelineRunFinalizer(pipelineRunName, pipelineRunApi)
			}(pvcSubPath, &wg, pipelinerun.GetName(), pipelineRunApi)
		}
	}

	log.Println("Wait cleanup all subpath folders....")
	wg.Wait()
	log.Println("Done!")
}

func watchNewPipelineRuns(pipelineRunApi v1beta1.PipelineRunInterface, resourceVersion string) {
	watch, err := pipelineRunApi.Watch(context.TODO(), metav1.ListOptions{
		ResourceVersion: resourceVersion,
	})
	if err != nil {
		log.Fatal(err)
	}

	for event := range watch.ResultChan() {
		if event.Type != watchapi.Added {
			continue
		}
		pipelinerun, ok := event.Object.(*pipelinev1.PipelineRun)
		if !ok {
			continue
		}

		log.Printf("Detected new running pipelinerun %s... Stop pod....", pipelinerun.GetName())
		// Stop appication, we shouldn't continue cleanup when new pipelinerun executed, because this
		// new pipelinerun will fail on the pvc without support parallel read/write operation from different pods
		os.Exit(0)
	}
}

func cleanUpSubpaths(pvcSubPath string, wg *sync.WaitGroup) {
	path := filepath.Join(pkg.SOURCE_VOLUME_DIR, pvcSubPath)
	info, err := os.Stat(path)
	if err != nil {
		log.Println(err)
		return
	}
	log.Printf("PVC subpath %s data size is %d", path, info.Size())

	log.Printf("Remove pvc subpath: %s", path)
	if err := os.RemoveAll(path); err != nil {
		log.Println(err.Error())
		return
	}
}

func removePipelineRunFinalizer(pipelineRunName string, pipelineRunApi v1beta1.PipelineRunInterface) {
	log.Printf("Remove finalizer from pipelineRun %s", pipelineRunName)

	pipelineRun, err := pipelineRunApi.Get(context.TODO(), pipelineRunName, metav1.GetOptions{})
	if err != nil {
		log.Println(err)
		return
	}

	var index int
	for i, finalizer := range pipelineRun.ObjectMeta.Finalizers {
		if finalizer == pkg.PIPELINERUN_FINALIZER_NAME {
			index = i
			break
		}
	}
	pipelineRun.ObjectMeta.Finalizers = append(pipelineRun.ObjectMeta.Finalizers[:index], pipelineRun.ObjectMeta.Finalizers[index+1:]...)
	if _, err := pipelineRunApi.Update(context.TODO(), pipelineRun, metav1.UpdateOptions{}); err != nil {
		log.Println(err.Error())
	}
}
