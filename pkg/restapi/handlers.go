package restapi

import (
	"context"
	"fmt"

	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/model"
	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/storage"
	"github.com/gin-gonic/gin"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned/typed/pipeline/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

func StorePVCSubPath(pipelineRunApi v1beta1.PipelineRunInterface, subPathStorage *storage.PVCSubPathsStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		pipelineRunName := c.Request.FormValue("pipeline-run-name")
		if pipelineRunName == "" {
			c.JSON(400, gin.H{
				"error": "Bad request",
			})
			return
		}

		fmt.Println(pipelineRunName)

		pipelineRun, err := pipelineRunApi.Get(context.TODO(), pipelineRunName, metav1.GetOptions{})
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
	}
}

func GetAllPipelineWithPVCSubPath(subPathStorage *storage.PVCSubPathsStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		subPaths, err := subPathStorage.GetAll()
		if err != nil {
			c.AbortWithError(500, err)
			return
		}
		c.JSON(200, subPaths)
	}
}

func DeleteNotUsedSubPaths(pipelineRunApi v1beta1.PipelineRunInterface, subPathStorage *storage.PVCSubPathsStorage, clientset *kubernetes.Clientset) gin.HandlerFunc {
	return func(c *gin.Context) {
		subPaths, err := subPathStorage.GetAll()
		if err != nil {
			c.AbortWithError(500, err)
			return
		}

		pvcToCleanUp := []*model.PVCSubPath{}
		for _, pvcSubPath := range subPaths {
			_, err := pipelineRunApi.Get(context.TODO(), pvcSubPath.PipelineRun, metav1.GetOptions{})
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
	}
}