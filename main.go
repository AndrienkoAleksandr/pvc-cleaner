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
	"github.com/gin-gonic/gin"
	"github.com/redhat-appstudio/pvc-cleaner/pkg"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/cleaner"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/k8s"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/storage"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"k8s.io/client-go/kubernetes"
	"log"
)

func main() {
	pkg.ParseFlags()
	config := k8s.GetClusterConfig()

	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	// create Tekton clientset
	tknClientset, err := versioned.NewForConfig(config)
	if err != nil {
		log.Fatalf("failed to create pipeline clientset %s", err)
	}

	namespace, err := k8s.GetNamespace()
	if err != nil {
		log.Fatalf("failed to create pipeline clientset %s", err)
	}

	pipelinesRunApi := tknClientset.TektonV1beta1().PipelineRuns(namespace)
	subPathStorage := storage.NewPVCSubPathsStorage()
	cleanerConf := k8s.NewCleanerConfig(clientset, namespace)
	cleanerConf.CreateIfNotPresent()

	pvc_cleaner := cleaner.NewPVCSubPathCleaner(pipelinesRunApi, subPathStorage, clientset, cleanerConf, namespace)
	// cleanup pvc periodically
	go pvc_cleaner.WatchNewPipelineRuns(subPathStorage)
	go pvc_cleaner.ScheduleCleanUpSubPathFoldersContent()
	go pvc_cleaner.WatchAndCleanUpSubPathFolders()

	r := gin.Default()

	r.GET("/ready", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "Application is ready",
		})
	})

	// listen and serve on 0.0.0.0:8080
	if err := r.Run(); err != nil {
		log.Fatal(err.Error())
	}
}
