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
	"testing"

	"github.com/redhat-appstudio/pvc-cleaner/pkg"
	"github.com/redhat-appstudio/pvc-cleaner/pkg/storage"
	"github.com/stretchr/testify/assert"
	pipelinev1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	tknfake "github.com/tektoncd/pipeline/pkg/client/clientset/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	k8stest "k8s.io/client-go/testing"
	"knative.dev/pkg/apis"
	"knative.dev/pkg/apis/duck/v1beta1"
)

const (
	namespace = "pvc-cleaner"
)

var (
	clientSet *fakeclientset.Clientset

	pipelineRun = &pipelinev1.PipelineRun{
		ObjectMeta: v1.ObjectMeta{
			Name: "pipelinerun-java",
			Namespace: namespace,
		},
		Spec: pipelinev1.PipelineRunSpec{
			Workspaces: []pipelinev1.WorkspaceBinding{
				{
					Name: pkg.SOURCE_WORKSPACE_NAME,
					SubPath: "/workspace/source/tmp-ttt",
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pkg.DEFAULT_WORKSPACE_PVC,
					},
				},
			},
		},
		Status: pipelinev1.PipelineRunStatus{
			Status: v1beta1.Status{
				Conditions: v1beta1.Conditions{
					apis.Condition{
						Reason: "Completed",
					},
				},
			},
		},
	}

)

func init() {
	var initObjs []runtime.Object
	
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: v1.ObjectMeta{
			Name: pkg.DEFAULT_WORKSPACE_PVC, 
			Namespace: namespace,
		},
	}

	initObjs = append(initObjs, pvc)
	clientSet = fakeclientset.NewSimpleClientset(initObjs...)
}

func TestSubPathFoldersShouldNotBeCleanedUpWhenThereIsCorrespondingPipelineRun(t *testing.T) {
	tknClientSet := tknfake.NewSimpleClientset(pipelineRun)

	pipelinesRunApi := tknClientSet.TektonV1beta1().PipelineRuns(namespace)
	pvcStorage := storage.NewPVCSubPathsStorage()

	cleaner := NewPVCSubPathCleaner(pipelinesRunApi, pvcStorage, clientSet.CoreV1(), clientSet.RbacV1(), namespace)

	cleaner.AddNewPVC(pipelineRun)
	cleaner.CleanupSubFolders()

	assert.Equal(t, len(cleaner.subPathStorage.GetAll()), 1, "storage should contain only one pvc subpath")
}

func TestSubPathFoldersShouldBeCleanedUpWhenPipelineRunWasRemoved(t *testing.T) {
	tknClientSet := tknfake.NewSimpleClientset(pipelineRun)
	pipelinesRunApi := tknClientSet.TektonV1beta1().PipelineRuns(namespace)

	watcher := watch.NewFake()
	clientSet.PrependWatchReactor("pods", k8stest.DefaultWatchReactor(watcher, nil))
	defer watcher.Stop()

	cleaner := NewPVCSubPathCleaner(pipelinesRunApi, storage.NewPVCSubPathsStorage(), clientSet.CoreV1(), clientSet.RbacV1(), namespace)

	cleaner.AddNewPVC(pipelineRun)

	// Delete pipelinerun
	if err := pipelinesRunApi.Delete(context.TODO(), pipelineRun.Name, v1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	done := make(chan bool)
	go func () {
		cleaner.CleanupSubFolders()
		done <- true
	}()
	
	watcher.Add(&corev1.Pod{
		Status: corev1.PodStatus{
			Phase: corev1.PodSucceeded,
		},
	})

	<- done

	assert.Empty(t, len(cleaner.subPathStorage.GetAll()), "pvc sub-path storage should be an empty")
}
