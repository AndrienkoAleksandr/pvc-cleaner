package restapi

import (
	"context"
	"fmt"

	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/model"
	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/storage"
	"github.com/gin-gonic/gin"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned/typed/pipeline/v1beta1"
	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/scheduler"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func DeleteNotUsedSubPaths(cleaner *scheduler.PVCSubPathCleaner) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := cleaner.DeleteNotUsedSubPaths();err != nil {
			c.AbortWithError(500, err)
			return
		}

		c.JSON(200, gin.H{"message:": "Executed delete pod to clean up pvc subfolders"})
	}
}