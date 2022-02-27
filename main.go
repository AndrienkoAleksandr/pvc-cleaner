package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/k8s"
	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/restapi"
	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/scheduler"
	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/storage"
	"github.com/gin-gonic/gin"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/homedir"
)

func main() {
	isOutSideClusterConfig := os.Getenv("OUTSIDE_CLUSTER")
	var config *rest.Config
	if isOutSideClusterConfig == "true" {
		var kubeconfig *string
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
		} else {
			kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
		}
		flag.Parse()

		config = k8s.GetOusideClusterConfig(*kubeconfig)
	} else {
		config = k8s.GetInsideClusterConfig()
	}

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
	if err := subPathStorage.Init(); err != nil {
		log.Fatalf("Failed to init database storage %s", err.Error())
	}

	cleaner := scheduler.NewPVCSubPathCleaner(pipelinesRunApi, subPathStorage, clientset)
	// cleanup pvc periodically
	go cleaner.Schedule()

	r := gin.Default()

	r.POST("/pipeline-run", restapi.StorePVCSubPath(pipelinesRunApi, subPathStorage))

	r.GET("/pipeline-run/list", restapi.GetAllPipelineWithPVCSubPath(subPathStorage))

	r.DELETE("/pipeline-run/list", restapi.DeleteNotUsedSubPaths(cleaner))

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
