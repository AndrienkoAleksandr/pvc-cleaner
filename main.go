package main

import (
	"context"
	// "flag"
	"fmt"
	"log"
	// "path/filepath"

	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/model"
	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/storage"
	"github.com/gin-gonic/gin"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	// "k8s.io/client-go/tools/clientcmd"
	// "k8s.io/client-go/util/homedir"
)

func main() {
	// var kubeconfig *string
	// if home := homedir.HomeDir(); home != "" {
	// 	kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	// } else {
	// 	kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	// }
	// flag.Parse()

	// // use the current context in kubeconfig
	// config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	// if err != nil {
	// 	panic(err.Error())
	// }

	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}

	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	cs, err := versioned.NewForConfig(config)
	if err != nil {
		log.Fatalf("failed to create pipeline clientset %s", err)
	}
	// todo set up current namespace....
	pipelinesRunApi := cs.TektonV1beta1().PipelineRuns("default")

	subPathStorage := storage.NewPVCSubPathsStorage()
	if err := subPathStorage.Init(); err != nil {
		log.Fatalf("Failed to init database storage %s", err.Error())
	}

	r := gin.Default()

	r.POST("/pipeline-run", func(c *gin.Context) {
		pipelineRunName := c.Request.FormValue("pipeline-run-name")
		if pipelineRunName == "" {
			c.JSON(400, gin.H{
				"error": "Bad request",
			})
			return
		}

		fmt.Println(pipelineRunName)

		pipelineRun, err := pipelinesRunApi.Get(context.TODO(), pipelineRunName, metav1.GetOptions{})
		if err != nil {
			c.AbortWithError(500, err)
			return
		}

		var subPath string
		for _, workspace := range pipelineRun.Spec.Workspaces {
			if workspace.Name == "workspace" {
				subPath = workspace.SubPath
				break
			}
		}

		if subPath != "" {
			pvcSubPath := &model.PVCSubPath{PipelineRun: pipelineRunName, PVCSubPath: subPath}
			if err := subPathStorage.AddPVCSubPath(pvcSubPath); err != nil {
				c.AbortWithError(500, err)
				return
			}
		}

		fmt.Println("Got it!" + pipelineRun.Name)
		c.JSON(201, gin.H{
			"message": "server got pipeline run name " + pipelineRunName,
		})
	})

	r.GET("/pipeline-run/list", func(c *gin.Context) {
		subPaths, err := subPathStorage.GetAll()
		if err != nil {
			c.AbortWithError(500, err)
			return
		}
		c.JSON(200, subPaths)
	})

	r.DELETE("/pipeline-run/list", func(c *gin.Context) {
		subPaths, err := subPathStorage.GetAll()
		if err != nil {
			c.AbortWithError(500, err)
			return
		}

		pvcToCleanUp := []*model.PVCSubPath{}
		for _, pvcSubPath := range subPaths {
			_, err := pipelinesRunApi.Get(context.TODO(), pvcSubPath.PipelineRun, metav1.GetOptions{})
			if errors.IsNotFound(err) {
				pvcToCleanUp = append(pvcToCleanUp, pvcSubPath)
			}
		}

		fmt.Println(pvcToCleanUp)

		if len(pvcToCleanUp) == 0 {
			c.JSON(200, gin.H{"message:": "Nothing to delete"})
			return
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

		deadline := int64(5400)
		pvcCleanerPod := &corev1.Pod{
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

		// todo set up namespace
		_, err = clientset.CoreV1().Pods("default").Create(context.TODO(), pvcCleanerPod, metav1.CreateOptions{})
		if err != nil {
			c.AbortWithError(500, err)
			return
		}

		c.JSON(200, gin.H{"message:": "Executed delete pod to clean up pvc subfolders"})
	})

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "Application is ready",
		})
	})

	// listen and serve on 0.0.0.0:8080 (for windows "localhost:8080")
	if err := r.Run(); err != nil {
		log.Fatal(err.Error())
	}
}
