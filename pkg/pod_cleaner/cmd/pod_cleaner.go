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
	"sync"

	"flag"
	"github.com/redhat-appstudio/pvc-cleaner/pkg"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/k8s"
	pipelinev1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned/typed/pipeline/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	watchapi "k8s.io/apimachinery/pkg/watch"
	"strings"
)

func main() {
	var pvcSubPaths []string
	flag.Func("pvc-subpaths", "List pvc subpaths to cleanup", func(flagValue string) error {
		pvcSubPaths = append(pvcSubPaths, strings.Fields(flagValue)...)
		return nil
	})

	pkg.ParseFlags()

	if len(pvcSubPaths) == 0 {
		log.Println("Nothing to cleanup")
		return
	}

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
	for _, pvcSubPath := range pvcSubPaths {
		wg.Add(1)

		log.Printf("Cleanup subpath %s", pvcSubPath)
		go cleanUpSubpaths(pvcSubPath, &wg)
	}

	log.Println("Wait cleanup all subpath folders...")
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
	defer wg.Done()

	info, err := os.Stat(pvcSubPath)
	if err != nil {
		log.Println(err)
		return
	}
	log.Printf("PVC subpath %s data size is %d", pvcSubPath, info.Size())

	log.Printf("Remove pvc subpath: %s", pvcSubPath)
	if err := os.RemoveAll(pvcSubPath); err != nil {
		log.Println(err.Error())
		return
	}
}
